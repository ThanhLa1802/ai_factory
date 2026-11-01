package inference

import (
	"bytes"
	"context"
	"image/color"
	"net"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	infra "github.com/ai-factory/go-server/internal/infrastructure/inference"
	pb "github.com/ai-factory/go-server/internal/infrastructure/inference/pb"
	"google.golang.org/grpc"
)

// captureBatchServer is a stand-in Python worker: it records every request it
// receives and answers each with a final END_TURN event. This exercises the
// real gRPC wire (client → scheduler → batch RPC), not just the in-process
// mapping helpers.
type captureBatchServer struct {
	pb.UnimplementedBatchInferenceServiceServer

	mu   sync.Mutex
	reqs []*pb.GenerateRequest
}

func (s *captureBatchServer) BatchGenerate(req *pb.BatchGenerateRequest, stream pb.BatchInferenceService_BatchGenerateServer) error {
	s.mu.Lock()
	s.reqs = append(s.reqs, req.GetRequests()...)
	s.mu.Unlock()

	for _, r := range req.GetRequests() {
		if err := stream.Send(&pb.BatchGenerateResponse{
			RequestId:    r.GetRequestId(),
			EventType:    pb.GenerateEventType_EVENT_FINAL,
			StopReason:   pb.StopReason_STOP_END_TURN,
			FinishReason: "stop",
		}); err != nil {
			return err
		}
	}
	return nil
}

func (s *captureBatchServer) snapshot() []*pb.GenerateRequest {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]*pb.GenerateRequest(nil), s.reqs...)
}

// newCaptureWorker starts the fake worker on an ephemeral port and returns a
// scheduler pointed at it.
func newCaptureWorker(t *testing.T) (*captureBatchServer, *infra.BatchScheduler) {
	t.Helper()
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	srv := grpc.NewServer()
	fake := &captureBatchServer{}
	pb.RegisterBatchInferenceServiceServer(srv, fake)
	go func() { _ = srv.Serve(lis) }()
	t.Cleanup(srv.Stop)

	client, err := infra.NewClient(lis.Addr().String())
	if err != nil {
		t.Fatalf("inference.NewClient: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })

	bs := infra.NewBatchScheduler(client)
	t.Cleanup(bs.Shutdown)
	return fake, bs
}

func runImageTurn(t *testing.T, loop *Loop, sess *Session, msg Message) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	for ev := range loop.RunStreaming(ctx, sess, msg, infra.DefaultSamplingParams(), nil) {
		if ev.Type == LoopEventError {
			t.Fatalf("loop error: %v", ev.Err)
		}
	}
}

// TestLoopSendsImageBytesToWorker: a message that references an upload on disk
// reaches the worker as inline bytes + mime type.
func TestLoopSendsImageBytesToWorker(t *testing.T) {
	dir := t.TempDir()
	store := NewDiskImageStore(dir, 2<<20)
	raw := testPNG(t, color.RGBA{R: 255, A: 255})
	ref, err := store.Save(context.Background(), raw, "image/png")
	if err != nil {
		t.Fatalf("save image: %v", err)
	}

	fake, bs := newCaptureWorker(t)
	loop := NewLoop(bs, NewLocalToolExecutor(t.TempDir()), store)
	sess := NewSession("s1", 8192)

	runImageTurn(t, loop, sess, Message{
		Role:    RoleUser,
		Content: "what is in this picture?",
		Images:  []MessageImage{ref},
	})

	reqs := fake.snapshot()
	if len(reqs) != 1 {
		t.Fatalf("worker received %d requests, want 1", len(reqs))
	}
	msgs := reqs[0].GetMessages()
	if len(msgs) == 0 {
		t.Fatal("request has no messages")
	}
	user := msgs[len(msgs)-1]
	if user.GetContent() != "what is in this picture?" {
		t.Errorf("content = %q", user.GetContent())
	}
	if len(user.GetImages()) != 1 {
		t.Fatalf("worker got %d images, want 1", len(user.GetImages()))
	}
	if got := user.GetImages()[0].GetMimeType(); got != "image/png" {
		t.Errorf("mime = %q, want image/png", got)
	}
	if got := user.GetImages()[0].GetData(); !bytes.Equal(got, raw) {
		t.Errorf("image bytes differ (got %d bytes, want %d)", len(got), len(raw))
	}
}

// TestLoopSkipsImageMissingOnDisk: a deleted upload must not fail the turn; the
// text still reaches the worker.
func TestLoopSkipsImageMissingOnDisk(t *testing.T) {
	dir := t.TempDir()
	store := NewDiskImageStore(dir, 2<<20)
	ref, err := store.Save(context.Background(), testPNG(t, color.RGBA{G: 255, A: 255}), "image/png")
	if err != nil {
		t.Fatalf("save image: %v", err)
	}
	if err := os.Remove(filepath.Join(dir, ref.ID+".png")); err != nil {
		t.Fatalf("remove upload: %v", err)
	}

	fake, bs := newCaptureWorker(t)
	loop := NewLoop(bs, NewLocalToolExecutor(t.TempDir()), store)
	sess := NewSession("s2", 8192)

	runImageTurn(t, loop, sess, Message{
		Role:    RoleUser,
		Content: "still there?",
		Images:  []MessageImage{ref},
	})

	reqs := fake.snapshot()
	if len(reqs) != 1 {
		t.Fatalf("worker received %d requests, want 1", len(reqs))
	}
	msgs := reqs[0].GetMessages()
	user := msgs[len(msgs)-1]
	if len(user.GetImages()) != 0 {
		t.Errorf("worker got %d images, want 0 after the file was deleted", len(user.GetImages()))
	}
	if user.GetContent() != "still there?" {
		t.Errorf("content = %q, want the text to survive", user.GetContent())
	}
}

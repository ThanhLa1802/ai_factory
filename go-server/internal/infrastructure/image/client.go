package image

import (
	"context"
	"fmt"
	"io"

	"github.com/ai-factory/go-server/internal/infrastructure/image/pb"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

// Client wraps the gRPC connection to the Python image worker (separate
// process/port from the text inference worker).
type Client struct {
	addr   string
	conn   *grpc.ClientConn
	client pb.ImageServiceClient
}

// NewClient dials the image worker.
func NewClient(addr string) (*Client, error) {
	conn, err := grpc.NewClient(addr,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithDefaultCallOptions(
			grpc.MaxCallRecvMsgSize(100*1024*1024),
			grpc.MaxCallSendMsgSize(10*1024*1024),
		),
	)
	if err != nil {
		return nil, fmt.Errorf("connect to image worker at %s: %w", addr, err)
	}
	return &Client{addr: addr, conn: conn, client: pb.NewImageServiceClient(conn)}, nil
}

// Close shuts down the gRPC connection.
func (c *Client) Close() error { return c.conn.Close() }

// GenerateRequest holds the parameters for one image generation.
type GenerateRequest struct {
	RequestID      string
	Prompt         string
	NegativePrompt string
	Width          int32
	Height         int32
	Steps          int32
	GuidanceScale  float32
	Seed           int64
	NumImages      int32
	Scheduler      string
}

// GenerateResult is a completed generation (one or more PNG-encoded images).
type GenerateResult struct {
	PNG       [][]byte
	Width     int32
	Height    int32
	Seed      int64
	Steps     int32
	Scheduler string
}

// ProgressEvent is one step report from Stream.
type ProgressEvent struct {
	Step       int32
	TotalSteps int32
	Done       bool
	Result     *GenerateResult
	Error      string
}

func toProtoRequest(req GenerateRequest) *pb.ImageRequest {
	return &pb.ImageRequest{
		RequestId:      req.RequestID,
		Prompt:         req.Prompt,
		NegativePrompt: req.NegativePrompt,
		Width:          req.Width,
		Height:         req.Height,
		Steps:          req.Steps,
		GuidanceScale:  req.GuidanceScale,
		Seed:           req.Seed,
		NumImages:      req.NumImages,
		Scheduler:      req.Scheduler,
	}
}

func fromProtoResult(r *pb.ImageResponse) *GenerateResult {
	if r == nil {
		return nil
	}
	return &GenerateResult{
		PNG:       r.Png,
		Width:     r.Width,
		Height:    r.Height,
		Seed:      r.Seed,
		Steps:     r.Steps,
		Scheduler: r.Scheduler,
	}
}

// Generate runs the pipeline to completion and returns the encoded images.
func (c *Client) Generate(ctx context.Context, req GenerateRequest) (*GenerateResult, error) {
	resp, err := c.client.GenerateImage(ctx, toProtoRequest(req))
	if err != nil {
		return nil, fmt.Errorf("generate image: %w", err)
	}
	return fromProtoResult(resp), nil
}

// Stream opens a streaming generation and returns a channel of progress events.
// The channel closes when generation completes. Cancel ctx to stop.
func (c *Client) Stream(ctx context.Context, req GenerateRequest) (<-chan ProgressEvent, error) {
	stream, err := c.client.StreamImage(ctx, toProtoRequest(req))
	if err != nil {
		return nil, fmt.Errorf("start image stream: %w", err)
	}

	events := make(chan ProgressEvent, 32)
	go func() {
		defer close(events)
		for {
			p, err := stream.Recv()
			if err == io.EOF {
				return
			}
			if err != nil {
				msg := err.Error()
				if ctx.Err() != nil {
					msg = "cancelled"
				}
				events <- ProgressEvent{Done: true, Error: msg}
				return
			}
			ev := ProgressEvent{
				Step:       p.Step,
				TotalSteps: p.TotalSteps,
				Done:       p.Done,
				Result:     fromProtoResult(p.Result),
				Error:      p.Error,
			}
			select {
			case events <- ev:
			case <-ctx.Done():
				return
			}
			if ev.Done {
				return
			}
		}
	}()
	return events, nil
}

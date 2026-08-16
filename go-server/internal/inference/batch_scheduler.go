package inference

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	pb "github.com/ai-factory/go-server/internal/inference/pb"
	"github.com/ai-factory/go-server/internal/observability"
	"github.com/google/uuid"
)

const (
	// DefaultBatchWindow is how long we wait to collect a batch.
	DefaultBatchWindow = 100 * time.Millisecond
	// DefaultMaxBatchSize prevents VRAM overflow.
	DefaultMaxBatchSize = 4
	// submitChCapacity bounds how many requests may sit in the scheduler's
	// pending queue before TrySubmit sheds load (backpressure).
	submitChCapacity = 100
)

// ErrOverloaded is returned by TrySubmit when the scheduler's pending queue is
// full. Callers should shed the request (e.g. respond 503) instead of blocking.
var ErrOverloaded = errors.New("batch scheduler overloaded")

// BatchScheduler collects concurrent inference requests into batches
// and dispatches them to the Python worker via gRPC BatchGenerate RPC.
//
// This is the core of "continuous batching" — instead of serializing requests
// (1 GPU = 1 request at a time), we batch them together so the GPU processes
// multiple requests in one forward pass.
//
// Architecture:
//
//	Handler 1 ──┐
//	Handler 2 ──┼──► submitCh ──► collector goroutine ──► gRPC BatchGenerate
//	Handler 3 ──┘        │                    │
//	                      │  100ms window      │  per-request_id routing
//	                      │                    │
//	                 events channels ◄─────────┘
type BatchScheduler struct {
	client *Client

	submitCh     chan *batchItem
	maxBatchSize int
	batchWindow  time.Duration

	// Track active batches for graceful shutdown
	wg sync.WaitGroup
}

// batchItem holds one pending request.
type batchItem struct {
	ctx    context.Context
	req    GenerateRequest
	events chan<- GenerateEvent
}

// newBatchScheduler builds a scheduler without starting the collector loop,
// so tests can exercise Submit/TrySubmit against a raw queue.
func newBatchScheduler(client *Client) *BatchScheduler {
	return &BatchScheduler{
		client:       client,
		submitCh:     make(chan *batchItem, submitChCapacity),
		maxBatchSize: DefaultMaxBatchSize,
		batchWindow:  DefaultBatchWindow,
	}
}

// NewBatchScheduler creates a batch scheduler.
func NewBatchScheduler(client *Client) *BatchScheduler {
	bs := newBatchScheduler(client)
	bs.wg.Add(1)
	go bs.collectorLoop()
	return bs
}

// SetMaxBatchSize configures max batch size (must be called before Submit).
func (bs *BatchScheduler) SetMaxBatchSize(n int) {
	bs.maxBatchSize = n
}

// SetBatchWindow configures the collection window.
func (bs *BatchScheduler) SetBatchWindow(d time.Duration) {
	bs.batchWindow = d
}

// Submit enqueues a request for batched inference.
// Returns a channel that receives streaming events (same interface as Client.GenerateStream).
// The channel is closed when generation completes or on fatal error.
func (bs *BatchScheduler) Submit(ctx context.Context, req GenerateRequest) <-chan GenerateEvent {
	events, _ := bs.TrySubmit(ctx, req)
	return events
}

// TrySubmit is the non-blocking variant of Submit (load shedding / backpressure).
// If the pending queue is full it returns ErrOverloaded immediately instead of
// blocking, so the caller can reject the request (e.g. 503) rather than pile up
// goroutines behind a saturated worker.
func (bs *BatchScheduler) TrySubmit(ctx context.Context, req GenerateRequest) (<-chan GenerateEvent, error) {
	events := make(chan GenerateEvent, 100)
	select {
	case bs.submitCh <- &batchItem{
		ctx:    ctx,
		req:    req,
		events: events,
	}:
		return events, nil
	default:
		return nil, ErrOverloaded
	}
}

// Shutdown gracefully stops the collector loop.
func (bs *BatchScheduler) Shutdown() {
	close(bs.submitCh)
	bs.wg.Wait()
}

// collectorLoop continuously collects requests and dispatches batches.
//
// Pattern:
//
//	Block until first request arrives
//	→ Collect more in batchWindow (or until maxBatchSize)
//	→ Dispatch batch via gRPC
//	→ Repeat
func (bs *BatchScheduler) collectorLoop() {
	defer bs.wg.Done()

	for {
		// Block until first request
		first := <-bs.submitCh
		if first == nil {
			return // shutdown
		}

		batch := []*batchItem{first}

		// Collect more requests within the window
		timer := time.NewTimer(bs.batchWindow)
	collectLoop:
		for len(batch) < bs.maxBatchSize {
			select {
			case item := <-bs.submitCh:
				if item == nil {
					// Shutdown — dispatch current batch first
					break collectLoop
				}
				batch = append(batch, item)

			case <-timer.C:
				break collectLoop
			}
		}
		timer.Stop()

		// Dispatch this batch in a goroutine
		bs.wg.Add(1)
		go bs.dispatchBatch(batch)
	}
}

// dispatchBatch sends a batch to Python and routes responses.
func (bs *BatchScheduler) dispatchBatch(batch []*batchItem) {
	defer bs.wg.Done()

	if len(batch) == 0 {
		return
	}

	batchID := uuid.New().String()[:8]
	// A6 trace: the batch is a child of the first request's span (agent.loop).
	bctx := context.Background()
	if len(batch) > 0 {
		bctx = batch[0].ctx
	}
	_, span := observability.StartSpan(bctx, "inference.batch", "batch_id", batchID, "batch_size", len(batch))
	defer span.End()
	slog.Info("dispatching batch", "batch_id", batchID, "requests", len(batch), "window", bs.batchWindow.String(), "max", bs.maxBatchSize)

	// Build index: request_id → batchItem
	index := make(map[string]*batchItem, len(batch))

	// Build proto batch request
	pbBatch := &pb.BatchGenerateRequest{
		BatchId: batchID,
	}
	for _, item := range batch {
		pbReq := buildProtoRequest(item.req)
		pbBatch.Requests = append(pbBatch.Requests, pbReq)
		index[item.req.RequestID] = item
	}

	// Open batch gRPC stream
	ctx := context.Background()
	stream, err := bs.client.BatchGenerate(ctx, pbBatch)
	if err != nil {
		slog.Error("batch gRPC error", "batch_id", batchID, "err", err)
		bs.failAll(batch, fmt.Errorf("batch generate failed: %w", err))
		return
	}

	// Read stream, route events by request_id
	totalTokens := 0
	for {
		pbResp, err := stream.Recv()
		if err != nil {
			// Stream ended (io.EOF) or error
			if isEOF(err) {
				slog.Info("batch complete", "batch_id", batchID, "requests", len(batch), "tokens", totalTokens)
			} else {
				slog.Error("batch stream error", "batch_id", batchID, "err", err)
				// On real error, fail only unfinished requests still in index
				for _, item := range index {
					select {
					case item.events <- GenerateEvent{
						Type:         "final",
						StopReason:   "STOP_ERROR",
						FinishReason: "error",
						Error:        err.Error(),
					}:
					default:
					}
					close(item.events)
				}
			}
			return
		}

		// Route event to correct request
		item, ok := index[pbResp.RequestId]
		if !ok {
			slog.Warn("unknown request_id in batch response", "batch_id", batchID, "request_id", pbResp.RequestId)
			continue
		}

		event := pbToGenerateEvent(pbResp)
		if event.Type == "token" {
			totalTokens++
		}

		select {
		case item.events <- event:
		case <-item.ctx.Done():
			// Request cancelled — skip
		}

		// Close channel on final event
		if event.Type == "final" {
			close(item.events)
			delete(index, pbResp.RequestId)
		}
	}
}

// failAll sends error events to all batch items.
func (bs *BatchScheduler) failAll(batch []*batchItem, err error) {
	for _, item := range batch {
		item.events <- GenerateEvent{
			Type:         "final",
			StopReason:   "STOP_ERROR",
			FinishReason: "error",
			Error:        err.Error(),
		}
		close(item.events)
	}
}

// buildProtoRequest converts our GenerateRequest to proto GenerateRequest.
func buildProtoRequest(req GenerateRequest) *pb.GenerateRequest {
	pbReq := &pb.GenerateRequest{
		RequestId:    req.RequestID,
		SessionId:    req.SessionID,
		SystemPrompt: req.SystemPrompt,
		SamplingParams: &pb.SamplingParams{
			MaxTokens:     req.SamplingParams.MaxTokens,
			Temperature:   req.SamplingParams.Temperature,
			TopP:          req.SamplingParams.TopP,
			TopK:          req.SamplingParams.TopK,
			StopSequences: req.SamplingParams.StopSequences,
		},
	}

	pbReq.Messages = make([]*pb.Message, len(req.Messages))
	for i, m := range req.Messages {
		pbMsg := &pb.Message{
			Role:       m.Role,
			Content:    m.Content,
			ToolCallId: m.ToolCallID,
			ToolResult: m.ToolResult,
			IsError:    m.IsError,
		}
		if len(m.ToolCalls) > 0 {
			pbMsg.ToolCalls = make([]*pb.ToolCall, len(m.ToolCalls))
			for j, tc := range m.ToolCalls {
				pbMsg.ToolCalls[j] = &pb.ToolCall{
					Id:        tc.ID,
					Name:      tc.Name,
					Arguments: tc.Arguments,
				}
			}
		}
		pbReq.Messages[i] = pbMsg
	}

	if len(req.Tools) > 0 {
		pbReq.Tools = make([]*pb.ToolDefinition, len(req.Tools))
		for i, t := range req.Tools {
			pbReq.Tools[i] = &pb.ToolDefinition{
				Name:        t.Name,
				Description: t.Description,
				Parameters:  t.Parameters,
			}
		}
	}

	return pbReq
}

// pbToGenerateEvent converts a batch response proto to GenerateEvent.
func pbToGenerateEvent(resp *pb.BatchGenerateResponse) GenerateEvent {
	event := GenerateEvent{}

	switch resp.EventType {
	case pb.GenerateEventType_EVENT_TOKEN:
		event.Type = "token"
		event.Token = resp.Token

	case pb.GenerateEventType_EVENT_TOOL_USE:
		event.Type = "tool_use"
		if resp.ToolUse != nil {
			event.ToolUse = &ToolUseEvent{
				ID:        resp.ToolUse.Id,
				Name:      resp.ToolUse.Name,
				Arguments: resp.ToolUse.Arguments,
			}
		}

	case pb.GenerateEventType_EVENT_FINAL:
		event.Type = "final"
		event.StopReason = resp.StopReason.String()
		event.FinishReason = resp.FinishReason
		if resp.Usage != nil {
			event.Usage = &Usage{
				PromptTokens:     resp.Usage.PromptTokens,
				CompletionTokens: resp.Usage.CompletionTokens,
				TotalTokens:      resp.Usage.TotalTokens,
			}
		}
		event.Token = resp.Token
	}

	return event
}

// isEOF checks if a stream error is just end-of-stream.
func isEOF(err error) bool {
	return err != nil && (err.Error() == "EOF" || err.Error() == "rpc error: code = Unavailable desc = EOF")
}

// To implement inference executor interface for backward compat.
// BatchScheduler can be used wherever the inference client was used.

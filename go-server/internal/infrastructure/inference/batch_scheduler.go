package inference

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	pb "github.com/ai-factory/go-server/internal/infrastructure/inference/pb"
	"github.com/ai-factory/go-server/internal/infrastructure/observability"
	"github.com/google/uuid"
)

const (
	// DefaultBatchWindow is how long we wait to collect a batch.
	DefaultBatchWindow = 100 * time.Millisecond
	// DefaultMaxBatchSize prevents VRAM overflow.
	DefaultMaxBatchSize = 4
	// DefaultMaxInFlightBatches is the number of batches allowed to be in flight
	// at the worker at once (the Batch Slot count). K=1 keeps GPU concurrency at
	// one forward pass (≤ DefaultMaxBatchSize requests), matching the worker's
	// real capacity.
	DefaultMaxInFlightBatches = 1
	// submitChCapacity bounds how many requests may sit in the scheduler's
	// pending queue before TrySubmit sheds load (backpressure).
	submitChCapacity = 100
)

// ErrOverloaded is returned by TrySubmit when the scheduler's pending queue is
// full. Callers should shed the request (e.g. respond 503) instead of blocking.
var ErrOverloaded = errors.New("batch scheduler overloaded")

// errSchedulerClosed is returned by TrySubmit after Shutdown.
var errSchedulerClosed = errors.New("batch scheduler closed")

// batchClient is the seam the scheduler needs from the inference client. The
// concrete *Client satisfies it; tests provide a scripted fake.
type batchClient interface {
	BatchGenerate(ctx context.Context, req *pb.BatchGenerateRequest) (batchStream, error)
}

// BatchScheduler collects concurrent inference requests into batches
// and dispatches them to the Python worker via gRPC BatchGenerate RPC.
//
// This is the core of "continuous batching" — instead of serializing requests
// (1 GPU = 1 request at a time), we batch them together so the GPU processes
// multiple requests in one forward pass.
//
// A fixed number of Batch Slots (maxInFlight) bounds how many batches may be in
// flight at the worker. The collector blocks once every slot is taken, which
// fills submitCh and eventually sheds load — real backpressure, not just a
// pending-queue counter.
//
// Architecture:
//
//	Handler 1 ──┐
//	Handler 2 ──┼──► submitCh ──► collector ──► [slots] ──► gRPC BatchGenerate
//	Handler 3 ──┘        │             │
//	                      │  100ms      │  per-request_id routing
//	                      │             │
//	                 events channels ◄──┘
type BatchScheduler struct {
	client batchClient

	submitCh     chan *batchItem
	maxBatchSize int
	maxInFlight  int
	batchWindow  time.Duration

	// slots bounds in-flight batches; stopCh unblocks the collector on Shutdown.
	slots    chan struct{}
	stopCh   chan struct{}
	stopOnce sync.Once

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
func newBatchScheduler(client batchClient) *BatchScheduler {
	maxInFlight := DefaultMaxInFlightBatches
	return &BatchScheduler{
		client:       client,
		submitCh:     make(chan *batchItem, submitChCapacity),
		maxBatchSize: DefaultMaxBatchSize,
		maxInFlight:  maxInFlight,
		batchWindow:  DefaultBatchWindow,
		slots:        make(chan struct{}, maxInFlight),
		stopCh:       make(chan struct{}),
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

// SetMaxInFlight configures the Batch Slot count (must be called before Submit).
func (bs *BatchScheduler) SetMaxInFlight(n int) {
	if n < 1 {
		n = 1
	}
	bs.maxInFlight = n
	bs.slots = make(chan struct{}, n)
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
	select {
	case <-bs.stopCh:
		return nil, errSchedulerClosed
	default:
	}
	events := make(chan GenerateEvent, 100)
	if ctx == nil {
		ctx = context.Background()
	}
	select {
	case bs.submitCh <- &batchItem{
		ctx:    ctx,
		req:    req,
		events: events,
	}:
		return events, nil
	case <-bs.stopCh:
		return nil, errSchedulerClosed
	default:
		return nil, ErrOverloaded
	}
}

// Shutdown gracefully stops the collector loop. Safe to call more than once.
func (bs *BatchScheduler) Shutdown() {
	bs.stopOnce.Do(func() { close(bs.stopCh) })
	bs.wg.Wait()
}

// collectorLoop continuously collects requests and dispatches batches.
//
// Pattern:
//
//	Block until first request arrives
//	→ Collect more in batchWindow (or until maxBatchSize)
//	→ Acquire a Batch Slot (blocking when the worker is saturated)
//	→ Dispatch batch via gRPC
//	→ Repeat
func (bs *BatchScheduler) collectorLoop() {
	defer bs.wg.Done()

	for {
		// Block until first request or shutdown.
		var first *batchItem
		select {
		case <-bs.stopCh:
			return
		case first = <-bs.submitCh:
		}
		if first == nil {
			return
		}

		batch := []*batchItem{first}

		// Collect more requests within the window.
		timer := time.NewTimer(bs.batchWindow)
	collectLoop:
		for len(batch) < bs.maxBatchSize {
			select {
			case item := <-bs.submitCh:
				if item == nil {
					break collectLoop
				}
				batch = append(batch, item)
			case <-timer.C:
				break collectLoop
			}
		}
		timer.Stop()

		// Acquire a Batch Slot. Blocking here is the point: once the worker is
		// saturated, the collector stops draining submitCh so the pending queue
		// fills and TrySubmit sheds load.
		select {
		case bs.slots <- struct{}{}:
		case <-bs.stopCh:
			bs.failAll(batch, errSchedulerClosed)
			return
		}

		bs.wg.Add(1)
		go bs.dispatchBatch(batch)
	}
}

// dispatchBatch sends a batch to Python and routes responses. It holds one
// Batch Slot for its lifetime.
func (bs *BatchScheduler) dispatchBatch(batch []*batchItem) {
	defer bs.wg.Done()
	defer func() { <-bs.slots }()

	if len(batch) == 0 {
		return
	}

	batchID := uuid.New().String()[:8]
	// A6 trace: the batch is a child of the first request's span (agent.loop).
	bctx := context.Background()
	if batch[0].ctx != nil {
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

	// Cancel the batch RPC once every request in it is cancelled (or on
	// shutdown) so an abandoned batch stops occupying the worker.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	bs.watchBatchCancelled(ctx, cancel, batch)

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
			// Stream ended (io.EOF) or error. Either way, every request still in
			// the index must be closed, or the consumer's `range` blocks forever.
			if isEOF(err) {
				slog.Info("batch complete", "batch_id", batchID, "requests", len(batch), "tokens", totalTokens)
				// index is normally empty by now; anything left never got a
				// final, so close it rather than leak the consumer's range.
				bs.finishAll(index, "STOP_ERROR", "error", "inference stream ended without final")
			} else if ctx.Err() != nil {
				// The batch RPC was cancelled (all requests gone or shutdown).
				slog.Info("batch cancelled", "batch_id", batchID)
				bs.finishAll(index, "STOP_CANCELLED", "cancelled", "")
			} else {
				slog.Error("batch stream error", "batch_id", batchID, "err", err)
				bs.finishAll(index, "STOP_ERROR", "error", err.Error())
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
			// Request cancelled — skip delivery but still close on final below.
		case <-bs.stopCh:
		}

		// Close channel on final event
		if event.Type == "final" {
			bs.closeItem(item)
			delete(index, pbResp.RequestId)
		}
	}
}

// watchBatchCancelled cancels the batch RPC once every request in the batch has
// been cancelled, or when the scheduler shuts down.
func (bs *BatchScheduler) watchBatchCancelled(ctx context.Context, cancel context.CancelFunc, batch []*batchItem) {
	remaining := int32(len(batch))
	for _, item := range batch {
		go func(it *batchItem) {
			var done <-chan struct{}
			if it.ctx != nil {
				done = it.ctx.Done()
			}
			select {
			case <-done:
			case <-bs.stopCh:
			case <-ctx.Done():
			}
			if atomic.AddInt32(&remaining, -1) == 0 {
				cancel()
			}
		}(item)
	}
}

// closeItem closes a request's event channel exactly once. The scheduler is the
// only closer, so a close is safe as long as each item is closed once.
func (bs *BatchScheduler) closeItem(item *batchItem) {
	defer func() { _ = recover() }() // defensive: never double-close a channel
	close(item.events)
}

// finishAll closes every request still pending in the index with a terminal
// final event. Used when a stream ends early or errors.
func (bs *BatchScheduler) finishAll(index map[string]*batchItem, stopReason, finishReason, errMsg string) {
	for id, item := range index {
		select {
		case item.events <- GenerateEvent{
			Type:         "final",
			StopReason:   stopReason,
			FinishReason: finishReason,
			Error:        errMsg,
		}:
		default:
		}
		bs.closeItem(item)
		delete(index, id)
	}
}

// failAll sends error events to all batch items and closes their channels.
// Sends are non-blocking so a stopped consumer can never wedge the scheduler.
func (bs *BatchScheduler) failAll(batch []*batchItem, err error) {
	stopReason, finishReason, msg := "STOP_ERROR", "error", err.Error()
	if errors.Is(err, errSchedulerClosed) {
		stopReason, finishReason, msg = "STOP_CANCELLED", "cancelled", ""
	}
	for _, item := range batch {
		select {
		case item.events <- GenerateEvent{
			Type:         "final",
			StopReason:   stopReason,
			FinishReason: finishReason,
			Error:        msg,
		}:
		default:
		}
		bs.closeItem(item)
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

	case pb.GenerateEventType_EVENT_REASONING:
		event.Type = "reasoning"
		event.Token = resp.ReasoningToken

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
	return errors.Is(err, io.EOF)
}

// To implement inference executor interface for backward compat.
// BatchScheduler can be used wherever the inference client was used.

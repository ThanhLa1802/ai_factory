package inference

import (
	"context"
	"fmt"
	"io"
	"log/slog"

	pb "github.com/ai-factory/go-server/internal/inference/pb"
	"github.com/ai-factory/go-server/internal/session"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

// Client wraps the gRPC connection to the Python inference worker.
type Client struct {
	addr        string
	conn        *grpc.ClientConn
	client      pb.InferenceServiceClient
	batchClient pb.BatchInferenceServiceClient
}

// NewClient creates a new inference client.
func NewClient(addr string) (*Client, error) {
	conn, err := grpc.NewClient(addr,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithDefaultCallOptions(
			grpc.MaxCallRecvMsgSize(100*1024*1024),
			grpc.MaxCallSendMsgSize(10*1024*1024),
		),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to connect to inference worker at %s: %w", addr, err)
	}

	return &Client{
		addr:        addr,
		conn:        conn,
		client:      pb.NewInferenceServiceClient(conn),
		batchClient: pb.NewBatchInferenceServiceClient(conn),
	}, nil
}

// Close shuts down the gRPC connection.
func (c *Client) Close() error {
	return c.conn.Close()
}

// GenerateRequest holds all parameters for an inference call.
type GenerateRequest struct {
	RequestID      string
	SessionID      string
	Messages       []session.Message
	SystemPrompt   string
	SamplingParams SamplingParams
	Tools          []session.ToolDefinition
}

// SamplingParams controls token generation.
type SamplingParams struct {
	MaxTokens     int32
	Temperature   float32
	TopP          float32
	TopK          int32
	StopSequences []string
}

// DefaultSamplingParams returns sensible defaults.
func DefaultSamplingParams() SamplingParams {
	return SamplingParams{
		MaxTokens:   1024,
		Temperature: 0.7,
		TopP:        0.9,
		TopK:        50,
	}
}

// GenerateEvent is a single event from the inference stream.
type GenerateEvent struct {
	Type         string // "token", "tool_use", "final"
	Token        string
	ToolUse      *ToolUseEvent
	StopReason   string
	FinishReason string
	Usage        *Usage
	Error        string
}

// ToolUseEvent represents a tool call from the model.
type ToolUseEvent struct {
	ID        string
	Name      string
	Arguments string
}

// Usage holds token counts.
type Usage struct {
	PromptTokens     int32
	CompletionTokens int32
	TotalTokens      int32
}

// BatchGenerate sends a batch of requests to the Python worker.
// Returns a stream of batch responses keyed by request_id for routing.
func (c *Client) BatchGenerate(ctx context.Context, req *pb.BatchGenerateRequest) (pb.BatchInferenceService_BatchGenerateClient, error) {
	return c.batchClient.BatchGenerate(ctx, req)
}

// GenerateStream opens a streaming inference connection to the Python worker.
// Returns a channel of events. The channel is closed when generation completes.
// Cancel the context to propagate cancellation to the Python worker.
func (c *Client) GenerateStream(ctx context.Context, req GenerateRequest) (<-chan GenerateEvent, error) {
	// Build proto request
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

	// Convert messages
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

	// Convert tools
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

	stream, err := c.client.Generate(ctx, pbReq)
	if err != nil {
		return nil, fmt.Errorf("failed to start generation: %w", err)
	}

	events := make(chan GenerateEvent, 100)

	go func() {
		defer close(events)

		for {
			pbResp, err := stream.Recv()
			if err == io.EOF {
				return
			}
			if err != nil {
				// Check if cancelled
				if ctx.Err() != nil {
					events <- GenerateEvent{
						Type:         "final",
						StopReason:   "STOP_CANCELLED",
						FinishReason: "cancelled",
					}
					return
				}
				slog.Error("inference stream error", "err", err)
				events <- GenerateEvent{
					Type:         "final",
					StopReason:   "STOP_ERROR",
					FinishReason: "error",
					Error:        err.Error(),
				}
				return
			}

			event := GenerateEvent{}

			switch pbResp.EventType {
			case pb.GenerateEventType_EVENT_TOKEN:
				event.Type = "token"
				event.Token = pbResp.Token

			case pb.GenerateEventType_EVENT_TOOL_USE:
				event.Type = "tool_use"
				if pbResp.ToolUse != nil {
					event.ToolUse = &ToolUseEvent{
						ID:        pbResp.ToolUse.Id,
						Name:      pbResp.ToolUse.Name,
						Arguments: pbResp.ToolUse.Arguments,
					}
				}

			case pb.GenerateEventType_EVENT_FINAL:
				event.Type = "final"
				event.StopReason = pbResp.StopReason.String()
				event.FinishReason = pbResp.FinishReason
				if pbResp.Usage != nil {
					event.Usage = &Usage{
						PromptTokens:     pbResp.Usage.PromptTokens,
						CompletionTokens: pbResp.Usage.CompletionTokens,
						TotalTokens:      pbResp.Usage.TotalTokens,
					}
				}
				event.Token = pbResp.Token // may contain error message
			}

			select {
			case events <- event:
			case <-ctx.Done():
				return
			}
		}
	}()

	return events, nil
}

package inference

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"

	infra "github.com/ai-factory/go-server/internal/infrastructure/inference"
	"github.com/ai-factory/go-server/internal/infrastructure/observability"
	"github.com/google/uuid"
)

const (
	// MaxToolIterations prevents infinite tool-calling loops.
	MaxToolIterations = 10
)

// ---------------------------------------------------------------------------
// Streaming event types
// ---------------------------------------------------------------------------

// LoopEventType classifies events emitted by the agentic loop.
type LoopEventType int

const (
	LoopEventToken      LoopEventType = iota // A single token from the model
	LoopEventToolUse                         // Model requested a tool call
	LoopEventToolResult                      // Tool execution result
	LoopEventFinal                           // Generation complete
	LoopEventError                           // Fatal error
	LoopEventReasoning                       // Reasoning token (display-only, not in session context)
)

// LoopEvent is a single streaming event from the agentic loop.
// The HTTP handler forwards these directly to SSE.
type LoopEvent struct {
	Type LoopEventType

	// Token — populated for LoopEventToken (one real token from gRPC stream).
	Token string

	// ToolUse — populated for LoopEventToolUse.
	ToolCall *ToolCall

	// ToolResult — populated for LoopEventToolResult.
	ToolResult string
	IsError    bool

	// Final — populated for LoopEventFinal.
	StopReason   string
	FinishReason string
	Usage        *infra.Usage

	// Error — populated for LoopEventError.
	Err error
}

// ---------------------------------------------------------------------------
// Loop
// ---------------------------------------------------------------------------

// Loop orchestrates the agentic conversation: user message → model → tools → model → ...
// Uses BatchScheduler for inference — individual GenerateStream calls are collected
// and batched together for GPU efficiency.
type Loop struct {
	scheduler *infra.BatchScheduler
	tools     ToolExecutor
}

// NewLoop creates a new agentic loop with batch scheduler.
func NewLoop(scheduler *infra.BatchScheduler, executor ToolExecutor) *Loop {
	return &Loop{
		scheduler: scheduler,
		tools:     executor,
	}
}

// RunStreaming executes the agentic loop with real-time token streaming.
//
// Events are pushed to the returned channel as they happen:
//   - Tokens are forwarded the moment they arrive from the gRPC stream
//   - Tool calls are emitted when detected
//   - Tool results are emitted after execution
//   - Final event signals completion
//
// The channel is closed when generation completes or on fatal error.
// Cancel the context to abort (cancel propagation → gRPC → Python worker).
func (l *Loop) RunStreaming(ctx context.Context, sess *Session, userMessage Message, params infra.SamplingParams) <-chan LoopEvent {
	events := make(chan LoopEvent, 64) // buffer to avoid blocking on token emission

	go func() {
		defer close(events)
		// A6 trace: one span per generation (may run multiple iterations).
		_, span := observability.StartSpan(ctx, "agent.loop", "session_id", sess.GetID())
		defer span.End()

		// Add user message to session
		sess.AddMessage(ctx, userMessage)

		for iteration := 0; iteration < MaxToolIterations; iteration++ {
			// Check context cancellation
			select {
			case <-ctx.Done():
				events <- LoopEvent{
					Type:         LoopEventFinal,
					StopReason:   "STOP_CANCELLED",
					FinishReason: "cancelled",
				}
				return
			default:
			}

			// Get current messages, truncate if needed
			allMessages := sess.GetMessages()
			if sess.EstimatedTokens() > int(float64(sess.MaxTokens)*DangerZoneBeforeTruncate) {
				allMessages = TruncateMessages(allMessages, sess.MaxTokens)
			}

			// Build tool definitions
			toolDefs := l.tools.ListTools()
			sessToolDefs := make([]infra.ToolDefinition, len(toolDefs))
			for i, td := range toolDefs {
				sessToolDefs[i] = infra.ToolDefinition{
					Name:        td.Name,
					Description: td.Description,
					Parameters:  td.Parameters,
				}
			}

			// Send to inference
			req := infra.GenerateRequest{
				RequestID:      uuid.New().String(),
				SessionID:      sess.GetID(),
				Messages:       toInferenceMessages(allMessages),
				SystemPrompt:   sess.SystemPrompt,
				SamplingParams: params,
				Tools:          sessToolDefs,
			}

			// Submit to batch scheduler — may wait up to 100ms to collect a batch.
			// TrySubmit sheds load (ErrOverloaded → 503) when the queue is full.
			grpcEvents, err := l.scheduler.TrySubmit(ctx, req)
			if err != nil {
				events <- LoopEvent{
					Type: LoopEventError,
					Err:  fmt.Errorf("submit inference: %w", err),
				}
				return
			}

			// Collect response while streaming tokens
			var (
				assistantContent string
				toolCalls        []ToolCall
				stopReason       string
				finishReason     string
				usage            *infra.Usage
			)

			for event := range grpcEvents {
				switch event.Type {
				case "token":
					// Forward token immediately — this is the key fix!
					assistantContent += event.Token
					events <- LoopEvent{
						Type:  LoopEventToken,
						Token: event.Token,
					}

				case "reasoning":
					// Reasoning token — forward for display only, do NOT add to session context.
					events <- LoopEvent{
						Type:  LoopEventReasoning,
						Token: event.Token,
					}

				case "tool_use":
					if event.ToolUse != nil {
						tc := ToolCall{
							ID:        event.ToolUse.ID,
							Name:      event.ToolUse.Name,
							Arguments: event.ToolUse.Arguments,
						}
						toolCalls = append(toolCalls, tc)
						events <- LoopEvent{
							Type:     LoopEventToolUse,
							ToolCall: &tc,
						}
					}

				case "final":
					stopReason = event.StopReason
					finishReason = event.FinishReason
					usage = event.Usage

					if event.StopReason == "STOP_ERROR" {
						slog.Error("inference error", "error", event.Error)
						events <- LoopEvent{
							Type: LoopEventError,
							Err:  errors.New(event.Error),
						}
						return
					}
				}
			}

			// Build and save assistant message
			assistantMsg := Message{
				Role:    RoleAssistant,
				Content: assistantContent,
			}
			if len(toolCalls) > 0 {
				assistantMsg.ToolCalls = toolCalls
			}
			sess.AddMessage(ctx, assistantMsg)

			// If model wants to call tools, execute them
			if stopReason == "STOP_TOOL_USE" && len(toolCalls) > 0 {
				for _, tc := range toolCalls {
					toolResult, execErr := l.tools.Execute(ctx, tc.Name, json.RawMessage(tc.Arguments))

					var resultText string
					var isError bool
					if execErr != nil {
						resultText = execErr.Error()
						isError = true
					} else if toolResult != nil {
						resultText = string(toolResult)
					}

					toolMsg := Message{
						Role:       RoleTool,
						ToolCallID: tc.ID,
						ToolResult: resultText,
						IsError:    isError,
					}
					sess.AddMessage(ctx, toolMsg)

					events <- LoopEvent{
						Type:       LoopEventToolResult,
						ToolResult: resultText,
						IsError:    isError,
					}
				}
				// Continue loop — model gets tool results in next iteration
				continue
			}

			// End turn — send final event and exit
			events <- LoopEvent{
				Type:         LoopEventFinal,
				StopReason:   stopReason,
				FinishReason: finishReason,
				Usage:        usage,
			}
			return
		}

		// Max iterations reached
		events <- LoopEvent{
			Type: LoopEventError,
			Err:  fmt.Errorf("reached max tool iterations (%d)", MaxToolIterations),
		}
	}()

	return events
}

// toInferenceMessages maps session messages onto the inference client's wire
// types. infrastructure/inference owns those types so it never imports a
// service package (design §4.2 / D-P4-4).
func toInferenceMessages(msgs []Message) []infra.Message {
	out := make([]infra.Message, len(msgs))
	for i, m := range msgs {
		out[i] = infra.Message{
			Role:       m.Role,
			Content:    m.Content,
			ToolCallID: m.ToolCallID,
			ToolResult: m.ToolResult,
			IsError:    m.IsError,
		}
		if len(m.ToolCalls) > 0 {
			out[i].ToolCalls = make([]infra.ToolCall, len(m.ToolCalls))
			for j, tc := range m.ToolCalls {
				out[i].ToolCalls[j] = infra.ToolCall{ID: tc.ID, Name: tc.Name, Arguments: tc.Arguments}
			}
		}
	}
	return out
}

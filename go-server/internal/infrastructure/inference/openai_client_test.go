package inference

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

// sseServer returns a test server that streams the given raw SSE payload.
func sseServer(t *testing.T, status int, payload string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(payload))
	}))
	t.Cleanup(srv.Close)
	return srv
}

func collect(t *testing.T, ch <-chan GenerateEvent) []GenerateEvent {
	t.Helper()
	var out []GenerateEvent
	for ev := range ch {
		out = append(out, ev)
	}
	return out
}

func TestOpenAIClientStreamsTokensAndFinal(t *testing.T) {
	payload := "" +
		"data: {\"choices\":[{\"delta\":{\"content\":\"Hel\"},\"finish_reason\":null}]}\n\n" +
		"data: {\"choices\":[{\"delta\":{\"content\":\"lo\"},\"finish_reason\":null}]}\n\n" +
		"data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n" +
		"data: {\"choices\":[],\"usage\":{\"prompt_tokens\":5,\"completion_tokens\":2,\"total_tokens\":7}}\n\n" +
		"data: [DONE]\n\n"
	srv := sseServer(t, http.StatusOK, payload)
	c := NewOpenAIClient(srv.URL, "test-model")

	ch, err := c.TrySubmit(context.Background(), GenerateRequest{
		Messages:       []Message{{Role: "user", Content: "hi"}},
		SamplingParams: DefaultSamplingParams(),
	})
	if err != nil {
		t.Fatalf("TrySubmit: %v", err)
	}
	events := collect(t, ch)
	if len(events) != 3 {
		t.Fatalf("got %d events, want 3: %+v", len(events), events)
	}
	if events[0].Type != "token" || events[0].Token != "Hel" {
		t.Errorf("event 0 = %+v", events[0])
	}
	if events[1].Type != "token" || events[1].Token != "lo" {
		t.Errorf("event 1 = %+v", events[1])
	}
	f := events[2]
	if f.Type != "final" || f.StopReason != "STOP_END_TURN" || f.FinishReason != "stop" {
		t.Errorf("final = %+v", f)
	}
	if f.Usage == nil || f.Usage.PromptTokens != 5 || f.Usage.TotalTokens != 7 {
		t.Errorf("usage = %+v", f.Usage)
	}
}

func TestOpenAIClientAccumulatesToolCalls(t *testing.T) {
	payload := "" +
		"data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":0,\"id\":\"call_1\",\"function\":{\"name\":\"read_file\",\"arguments\":\"{\\\"pat\"}}]},\"finish_reason\":null}]}\n\n" +
		"data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":0,\"function\":{\"arguments\":\"h\\\":\\\"a.txt\\\"}\"}}]},\"finish_reason\":null}]}\n\n" +
		"data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"tool_calls\"}]}\n\n" +
		"data: [DONE]\n\n"
	srv := sseServer(t, http.StatusOK, payload)
	c := NewOpenAIClient(srv.URL, "test-model")

	ch, err := c.TrySubmit(context.Background(), GenerateRequest{Messages: []Message{{Role: "user", Content: "hi"}}})
	if err != nil {
		t.Fatalf("TrySubmit: %v", err)
	}
	events := collect(t, ch)
	if len(events) != 2 {
		t.Fatalf("got %d events, want 2 (tool_use+final): %+v", len(events), events)
	}
	if events[0].Type != "tool_use" || events[0].ToolUse == nil {
		t.Fatalf("event 0 = %+v", events[0])
	}
	if events[0].ToolUse.Name != "read_file" || events[0].ToolUse.Arguments != `{"path":"a.txt"}` {
		t.Errorf("tool_use = %+v", events[0].ToolUse)
	}
	if events[1].StopReason != "STOP_TOOL_USE" {
		t.Errorf("stop reason = %q", events[1].StopReason)
	}
}

func TestOpenAIClientOverloaded(t *testing.T) {
	srv := sseServer(t, http.StatusServiceUnavailable, "busy")
	c := NewOpenAIClient(srv.URL, "test-model")
	_, err := c.TrySubmit(context.Background(), GenerateRequest{})
	if !errors.Is(err, ErrOverloaded) {
		t.Fatalf("err = %v, want ErrOverloaded", err)
	}
}

func TestOpenAIClientHTTPError(t *testing.T) {
	srv := sseServer(t, http.StatusInternalServerError, "boom")
	c := NewOpenAIClient(srv.URL, "test-model")
	_, err := c.TrySubmit(context.Background(), GenerateRequest{})
	if err == nil || errors.Is(err, ErrOverloaded) {
		t.Fatalf("err = %v, want a non-overload error", err)
	}
}

func TestOpenAIClientBuildBody(t *testing.T) {
	c := NewOpenAIClient("http://up", "served-model")
	raw, err := c.buildBody(GenerateRequest{
		SystemPrompt: "be nice",
		Messages: []Message{
			{Role: "user", Content: "hi"},
			{Role: "assistant", ToolCalls: []ToolCall{{ID: "c1", Name: "read_file", Arguments: "{}"}}},
			{Role: "tool", ToolCallID: "c1", ToolResult: "OK"},
		},
		SamplingParams: SamplingParams{MaxTokens: 10, Temperature: 0.5, TopP: 0.9, TopK: 40, StopSequences: []string{"END"}},
		Tools:          []ToolDefinition{{Name: "read_file", Description: "read", Parameters: `{"type":"object"}`}},
	})
	if err != nil {
		t.Fatalf("buildBody: %v", err)
	}
	var body map[string]any
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if body["model"] != "served-model" {
		t.Errorf("model = %v", body["model"])
	}
	if body["tool_choice"] != "auto" || body["tools"] == nil {
		t.Errorf("tools/tool_choice missing: %v", body)
	}
	msgs := body["messages"].([]any)
	if len(msgs) != 4 || msgs[0].(map[string]any)["role"] != "system" {
		t.Errorf("messages = %+v", msgs)
	}
	tool := msgs[3].(map[string]any)
	if tool["role"] != "tool" || tool["tool_call_id"] != "c1" || tool["content"] != "OK" {
		t.Errorf("tool message = %+v", tool)
	}
}

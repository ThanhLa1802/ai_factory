package inference

import (
	"encoding/json"
	"testing"

	infra "github.com/ai-factory/go-server/internal/infrastructure/inference"
)

func TestOpenAIToolsToInternal(t *testing.T) {
	if got := OpenAIToolsToInternal(nil); got != nil {
		t.Fatalf("empty tools = %v, want nil", got)
	}

	got := OpenAIToolsToInternal([]OpenAITool{
		{Type: "function", Function: OpenAIFunctionDef{
			Name:        "get_weather",
			Description: "Look up weather",
			Parameters:  json.RawMessage(`{"type":"object"}`),
		}},
		{Type: "function", Function: OpenAIFunctionDef{Description: "no name"}}, // dropped
	})
	if len(got) != 1 {
		t.Fatalf("len = %d, want 1 (nameless tool dropped)", len(got))
	}
	if got[0].Name != "get_weather" || got[0].Description != "Look up weather" ||
		got[0].Parameters != `{"type":"object"}` {
		t.Fatalf("unexpected definition: %+v", got[0])
	}
}

func TestToolDefinitionsForClientToolsReplaceBuiltins(t *testing.T) {
	exec := NewLocalToolExecutor(t.TempDir())
	client := []infra.ToolDefinition{
		{Name: "get_weather", Description: "client tool", Parameters: `{}`},
		{Name: "read_file", Description: "client override", Parameters: `{"override":true}`},
	}

	defs := toolDefinitionsFor(exec, client)

	if len(defs) != 2 {
		t.Fatalf("len = %d, want 2 (built-ins are not advertised when the client declares tools)", len(defs))
	}
	byName := make(map[string]infra.ToolDefinition, len(defs))
	for _, d := range defs {
		if _, dup := byName[d.Name]; dup {
			t.Fatalf("duplicate tool %q", d.Name)
		}
		byName[d.Name] = d
	}
	if _, ok := byName["run_command"]; ok {
		t.Error("built-in run_command must not leak into a client-tool request")
	}
	if byName["read_file"].Parameters != `{"override":true}` {
		t.Errorf("client definition must be passed through verbatim, got %q", byName["read_file"].Parameters)
	}
}

func TestToolDefinitionsForBuiltinsWhenNoClientTools(t *testing.T) {
	exec := NewLocalToolExecutor(t.TempDir())
	defs := toolDefinitionsFor(exec, nil)
	if len(defs) != 4 {
		t.Fatalf("len = %d, want the 4 built-ins", len(defs))
	}
}

func TestAllExecutable(t *testing.T) {
	l := &Loop{tools: NewLocalToolExecutor(t.TempDir())}

	if !l.allExecutable([]ToolCall{{Name: "read_file"}, {Name: "list_files"}}) {
		t.Fatal("built-in tools should be executable")
	}
	if l.allExecutable([]ToolCall{{Name: "read_file"}, {Name: "get_weather"}}) {
		t.Fatal("client-owned tool should not be reported executable")
	}
}

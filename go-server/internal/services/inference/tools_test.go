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

func TestToolDefinitionsForMergesClientTools(t *testing.T) {
	exec := NewLocalToolExecutor(t.TempDir())
	client := []infra.ToolDefinition{
		{Name: "get_weather", Description: "client tool", Parameters: `{}`},
		{Name: "read_file", Description: "client override", Parameters: `{"override":true}`},
	}

	defs := toolDefinitionsFor(exec, client)

	byName := make(map[string]infra.ToolDefinition, len(defs))
	for _, d := range defs {
		if _, dup := byName[d.Name]; dup {
			t.Fatalf("duplicate tool %q", d.Name)
		}
		byName[d.Name] = d
	}
	if len(defs) != 5 { // 4 built-ins + 1 new client tool
		t.Fatalf("len = %d, want 5", len(defs))
	}
	if byName["read_file"].Description != "client override" {
		t.Errorf("client definition should win on a name clash, got %q", byName["read_file"].Description)
	}
	if _, ok := byName["run_command"]; !ok {
		t.Error("built-in run_command missing from merged definitions")
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

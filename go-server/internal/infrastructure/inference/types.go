package inference

// Wire types owned by the inference client. They are deliberately independent
// of any service package so this infrastructure package never imports upward
// into services (design §4.2). Callers map their domain types onto these.

// Message is one conversation message sent to the worker.
type Message struct {
	Role       string
	Content    string
	ToolCalls  []ToolCall
	ToolCallID string
	ToolResult string
	IsError    bool
}

// ToolCall is a tool invocation requested by the model.
type ToolCall struct {
	ID        string
	Name      string
	Arguments string
}

// ToolDefinition declares a tool available to the model.
type ToolDefinition struct {
	Name        string
	Description string
	Parameters  string
}

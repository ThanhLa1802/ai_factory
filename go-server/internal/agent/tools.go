package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"
)

// ToolExecutor defines the interface for executing tools.
// Implementations: LocalToolExecutor, DockerToolExecutor (future), etc.
type ToolExecutor interface {
	// Execute runs a tool and returns the result as a JSON string.
	Execute(ctx context.Context, toolName string, params json.RawMessage) (json.RawMessage, error)
	// ListTools returns all available tool definitions.
	ListTools() []ToolDefinition
}

// ToolDefinition matches the session.ToolDefinition format used across the system.
type ToolDefinition struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Parameters  string `json:"parameters"` // JSON Schema
}

// ---------------------------------------------------------------------------
// LocalToolExecutor — runs tools directly on the host machine.
// ---------------------------------------------------------------------------

// LocalToolExecutor executes tools as local processes.
// In production, this would be sandboxed (Docker, etc.).
type LocalToolExecutor struct {
	tools       map[string]localTool
	workDir     string
	commandTimeout time.Duration
}

type localTool struct {
	def    ToolDefinition
	run    func(ctx context.Context, params json.RawMessage) (json.RawMessage, error)
}

// NewLocalToolExecutor creates an executor with built-in tools.
func NewLocalToolExecutor(workDir string) *LocalToolExecutor {
	e := &LocalToolExecutor{
		tools:          make(map[string]localTool),
		workDir:        workDir,
		commandTimeout: 30 * time.Second,
	}
	e.registerBuiltinTools()
	return e
}

func (e *LocalToolExecutor) registerBuiltinTools() {
	// read_file
	e.tools["read_file"] = localTool{
		def: ToolDefinition{
			Name:        "read_file",
			Description: "Read the contents of a file. Returns the file content as text.",
			Parameters:  `{"type":"object","properties":{"path":{"type":"string","description":"Path to the file to read"}},"required":["path"]}`,
		},
		run: e.runReadFile,
	}

	// write_file
	e.tools["write_file"] = localTool{
		def: ToolDefinition{
			Name:        "write_file",
			Description: "Write content to a file. Creates or overwrites the file.",
			Parameters:  `{"type":"object","properties":{"path":{"type":"string","description":"Path to write to"},"content":{"type":"string","description":"Content to write"}},"required":["path","content"]}`,
		},
		run: e.runWriteFile,
	}

	// run_command
	e.tools["run_command"] = localTool{
		def: ToolDefinition{
			Name:        "run_command",
			Description: "Run a shell command and return its output.",
			Parameters:  `{"type":"object","properties":{"command":{"type":"string","description":"The command to execute"},"workdir":{"type":"string","description":"Optional working directory"}},"required":["command"]}`,
		},
		run: e.runCommand,
	}

	// list_files
	e.tools["list_files"] = localTool{
		def: ToolDefinition{
			Name:        "list_files",
			Description: "List files in a directory.",
			Parameters:  `{"type":"object","properties":{"path":{"type":"string","description":"Directory path to list"},"pattern":{"type":"string","description":"Optional glob pattern"}},"required":["path"]}`,
		},
		run: e.runListFiles,
	}
}

// Execute runs a tool by name.
func (e *LocalToolExecutor) Execute(ctx context.Context, toolName string, params json.RawMessage) (json.RawMessage, error) {
	tool, ok := e.tools[toolName]
	if !ok {
		return nil, fmt.Errorf("unknown tool: %s (available: %s)", toolName, e.toolNames())
	}

	ctx, cancel := context.WithTimeout(ctx, e.commandTimeout)
	defer cancel()

	return tool.run(ctx, params)
}

// ListTools returns all available tool definitions.
func (e *LocalToolExecutor) ListTools() []ToolDefinition {
	defs := make([]ToolDefinition, 0, len(e.tools))
	for _, t := range e.tools {
		defs = append(defs, t.def)
	}
	return defs
}

func (e *LocalToolExecutor) toolNames() string {
	names := make([]string, 0, len(e.tools))
	for n := range e.tools {
		names = append(names, n)
	}
	return strings.Join(names, ", ")
}

// ---------------------------------------------------------------------------
// Tool implementations
// ---------------------------------------------------------------------------

type readFileParams struct {
	Path string `json:"path"`
}

func (e *LocalToolExecutor) runReadFile(ctx context.Context, raw json.RawMessage) (json.RawMessage, error) {
	var p readFileParams
	if err := json.Unmarshal(raw, &p); err != nil {
		return jsonError("invalid params: " + err.Error())
	}
	if p.Path == "" {
		return jsonError("path is required")
	}

	data, err := os.ReadFile(p.Path)
	if err != nil {
		return jsonError("read_file failed: " + err.Error())
	}
	return jsonResult(string(data))
}

type writeFileParams struct {
	Path    string `json:"path"`
	Content string `json:"content"`
}

func (e *LocalToolExecutor) runWriteFile(ctx context.Context, raw json.RawMessage) (json.RawMessage, error) {
	var p writeFileParams
	if err := json.Unmarshal(raw, &p); err != nil {
		return jsonError("invalid params: " + err.Error())
	}
	if p.Path == "" {
		return jsonError("path is required")
	}
	if err := os.WriteFile(p.Path, []byte(p.Content), 0644); err != nil {
		return jsonError("write_file failed: " + err.Error())
	}
	return jsonResult(fmt.Sprintf("Wrote %d bytes to %s", len(p.Content), p.Path))
}

type runCommandParams struct {
	Command string `json:"command"`
	WorkDir string `json:"workdir,omitempty"`
}

func (e *LocalToolExecutor) runCommand(ctx context.Context, raw json.RawMessage) (json.RawMessage, error) {
	var p runCommandParams
	if err := json.Unmarshal(raw, &p); err != nil {
		return jsonError("invalid params: " + err.Error())
	}
	if p.Command == "" {
		return jsonError("command is required")
	}

	cmd := exec.CommandContext(ctx, "sh", "-c", p.Command)
	if p.WorkDir != "" {
		cmd.Dir = p.WorkDir
	} else {
		cmd.Dir = e.workDir
	}

	output, err := cmd.CombinedOutput()
	if err != nil {
		return jsonError(fmt.Sprintf("command failed: %s\nOutput: %s", err.Error(), string(output)))
	}
	return jsonResult(string(output))
}

type listFilesParams struct {
	Path    string `json:"path"`
	Pattern string `json:"pattern,omitempty"`
}

func (e *LocalToolExecutor) runListFiles(ctx context.Context, raw json.RawMessage) (json.RawMessage, error) {
	var p listFilesParams
	if err := json.Unmarshal(raw, &p); err != nil {
		return jsonError("invalid params: " + err.Error())
	}
	if p.Path == "" {
		return jsonError("path is required")
	}

	entries, err := os.ReadDir(p.Path)
	if err != nil {
		return jsonError("list_files failed: " + err.Error())
	}

	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() {
			name += "/"
		}
		names = append(names, name)
	}

	return jsonResult(strings.Join(names, "\n"))
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func jsonResult(msg string) (json.RawMessage, error) {
	data, _ := json.Marshal(map[string]string{"result": msg})
	return json.RawMessage(data), nil
}

func jsonError(msg string) (json.RawMessage, error) {
	data, _ := json.Marshal(map[string]string{"error": msg})
	return json.RawMessage(data), nil
}

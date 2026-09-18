package inference

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// DockerToolExecutor runs the built-in tools inside a throwaway container, so a
// model-issued command cannot reach the host outside the mounted workspace.
//
// Isolation per call: no network, read-only root filesystem (the workspace is
// bind-mounted rw), all capabilities dropped, no-new-privileges, pid/memory/cpu
// caps. The image must ship a POSIX `sh` (default alpine). This is the sandboxed
// counterpart of LocalToolExecutor; pick it with tools.executor=docker.
type DockerToolExecutor struct {
	workDir string
	image   string
	timeout time.Duration
	tools   map[string]dockerTool

	// run invokes the docker CLI; tests swap it to assert argv without a daemon.
	run func(ctx context.Context, args []string, stdin io.Reader) ([]byte, error)
}

// dockerTool pairs a tool definition with the container invocation it builds.
type dockerTool struct {
	def   ToolDefinition
	build func(raw json.RawMessage) (dockerInvocation, error)
}

// dockerInvocation is the container-side of one tool call.
type dockerInvocation struct {
	script  string
	env     []string
	stdin   string
	workdir string // container cwd; "" means the workspace root
}

const (
	workspaceMount  = "/workspace"
	dockerToolLimit = 30 * time.Second
)

// NewDockerToolExecutor creates a sandboxed executor. workDir is mounted at
// /workspace (rw); image defaults to alpine when empty.
func NewDockerToolExecutor(workDir, image string) *DockerToolExecutor {
	if image == "" {
		image = "alpine:3.24"
	}
	if workDir == "" {
		workDir = "."
	}
	if abs, err := filepath.Abs(workDir); err == nil {
		workDir = abs
	}
	e := &DockerToolExecutor{
		workDir: workDir,
		image:   image,
		timeout: dockerToolLimit,
		tools:   make(map[string]dockerTool),
	}
	e.run = e.runDocker
	e.registerTools()
	return e
}

func (e *DockerToolExecutor) registerTools() {
	e.tools["read_file"] = dockerTool{
		def: ToolDefinition{
			Name:        "read_file",
			Description: "Read the contents of a file. Returns the file content as text.",
			Parameters:  `{"type":"object","properties":{"path":{"type":"string","description":"Path to the file to read"}},"required":["path"]}`,
		},
		build: func(raw json.RawMessage) (dockerInvocation, error) {
			var p readFileParams
			if err := json.Unmarshal(raw, &p); err != nil {
				return dockerInvocation{}, fmt.Errorf("invalid params: %w", err)
			}
			path, err := e.containerPath(p.Path)
			if err != nil {
				return dockerInvocation{}, err
			}
			return dockerInvocation{
				script: `cat -- "$AF_TOOL_PATH"`,
				env:    []string{"AF_TOOL_PATH=" + path},
			}, nil
		},
	}

	e.tools["write_file"] = dockerTool{
		def: ToolDefinition{
			Name:        "write_file",
			Description: "Write content to a file. Creates or overwrites the file.",
			Parameters:  `{"type":"object","properties":{"path":{"type":"string","description":"Path to write to"},"content":{"type":"string","description":"Content to write"}},"required":["path","content"]}`,
		},
		build: func(raw json.RawMessage) (dockerInvocation, error) {
			var p writeFileParams
			if err := json.Unmarshal(raw, &p); err != nil {
				return dockerInvocation{}, fmt.Errorf("invalid params: %w", err)
			}
			path, err := e.containerPath(p.Path)
			if err != nil {
				return dockerInvocation{}, err
			}
			return dockerInvocation{
				script: `cat > "$AF_TOOL_PATH" && printf 'Wrote %s bytes to %s\n' "$(wc -c < "$AF_TOOL_PATH" | tr -d ' ')" "$AF_TOOL_PATH"`,
				env:    []string{"AF_TOOL_PATH=" + path},
				stdin:  p.Content,
			}, nil
		},
	}

	e.tools["run_command"] = dockerTool{
		def: ToolDefinition{
			Name:        "run_command",
			Description: "Run a shell command and return its output.",
			Parameters:  `{"type":"object","properties":{"command":{"type":"string","description":"The command to execute"},"workdir":{"type":"string","description":"Optional working directory"}},"required":["command"]}`,
		},
		build: func(raw json.RawMessage) (dockerInvocation, error) {
			var p runCommandParams
			if err := json.Unmarshal(raw, &p); err != nil {
				return dockerInvocation{}, fmt.Errorf("invalid params: %w", err)
			}
			if p.Command == "" {
				return dockerInvocation{}, fmt.Errorf("command is required")
			}
			wd := ""
			if p.WorkDir != "" {
				resolved, err := e.containerPath(p.WorkDir)
				if err != nil {
					return dockerInvocation{}, err
				}
				wd = resolved
			}
			return dockerInvocation{
				script:  `sh -c "$AF_TOOL_CMD"`,
				env:     []string{"AF_TOOL_CMD=" + p.Command},
				workdir: wd,
			}, nil
		},
	}

	e.tools["list_files"] = dockerTool{
		def: ToolDefinition{
			Name:        "list_files",
			Description: "List files in a directory.",
			Parameters:  `{"type":"object","properties":{"path":{"type":"string","description":"Directory path to list"},"pattern":{"type":"string","description":"Optional glob pattern"}},"required":["path"]}`,
		},
		build: func(raw json.RawMessage) (dockerInvocation, error) {
			var p listFilesParams
			if err := json.Unmarshal(raw, &p); err != nil {
				return dockerInvocation{}, fmt.Errorf("invalid params: %w", err)
			}
			dir, err := e.containerPath(p.Path)
			if err != nil {
				return dockerInvocation{}, err
			}
			return dockerInvocation{
				script: `ls -1 -p -A -- "$AF_TOOL_DIR"`,
				env:    []string{"AF_TOOL_DIR=" + dir},
			}, nil
		},
	}
}

// Execute runs a tool inside a throwaway container. Tool-level failures (bad
// params, docker missing, non-zero exit) are returned as a JSON error result so
// the agentic loop can feed them back to the model instead of aborting.
func (e *DockerToolExecutor) Execute(ctx context.Context, toolName string, params json.RawMessage) (json.RawMessage, error) {
	tool, ok := e.tools[toolName]
	if !ok {
		return nil, fmt.Errorf("unknown tool: %s (available: %s)", toolName, e.toolNames())
	}

	inv, err := tool.build(params)
	if err != nil {
		return jsonError(err.Error())
	}

	ctx, cancel := context.WithTimeout(ctx, e.timeout)
	defer cancel()

	var stdin io.Reader
	if inv.stdin != "" {
		stdin = strings.NewReader(inv.stdin)
	}
	out, err := e.run(ctx, e.containerArgs(inv), stdin)
	if err != nil {
		return jsonError(fmt.Sprintf("docker %s failed: %s\nOutput: %s", toolName, err.Error(), string(out)))
	}

	result := string(out)
	if toolName != "read_file" {
		result = strings.TrimRight(result, "\n")
	}
	return jsonResult(result)
}

// ListTools returns all available tool definitions.
func (e *DockerToolExecutor) ListTools() []ToolDefinition {
	defs := make([]ToolDefinition, 0, len(e.tools))
	for _, t := range e.tools {
		defs = append(defs, t.def)
	}
	return defs
}

// CanExecute reports whether a sandboxed tool with this name exists.
func (e *DockerToolExecutor) CanExecute(toolName string) bool {
	_, ok := e.tools[toolName]
	return ok
}

// containerArgs assembles the `docker run` argv for one invocation.
func (e *DockerToolExecutor) containerArgs(inv dockerInvocation) []string {
	wd := inv.workdir
	if wd == "" {
		wd = workspaceMount
	}
	args := []string{
		"run", "--rm",
		"--network=none",
		"--read-only",
		"--tmpfs", "/tmp:rw,size=64m",
		"--cap-drop=ALL",
		"--security-opt=no-new-privileges",
		"--pids-limit=256",
		"--memory=512m",
		"--cpus=1",
		"-v", e.workDir + ":" + workspaceMount + ":rw",
		"-w", wd,
	}
	for _, kv := range inv.env {
		args = append(args, "-e", kv)
	}
	return append(args, e.image, "sh", "-c", inv.script)
}

func (e *DockerToolExecutor) runDocker(ctx context.Context, args []string, stdin io.Reader) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "docker", args...)
	if stdin != nil {
		cmd.Stdin = stdin
	}
	return cmd.CombinedOutput()
}

// containerPath maps a request path into the mounted workspace and rejects
// escapes. Paths are workspace-relative; a leading "/" is treated as the
// workspace root (there is no host path to address inside the sandbox).
func (e *DockerToolExecutor) containerPath(rel string) (string, error) {
	if rel == "" {
		return "", fmt.Errorf("path is required")
	}
	if vol := filepath.VolumeName(rel); vol != "" || strings.ContainsAny(rel, `\:`) {
		return "", fmt.Errorf("absolute or invalid path is not allowed: %s", rel)
	}
	cleaned := filepath.ToSlash(filepath.Clean(rel))
	cleaned = strings.TrimPrefix(cleaned, "/")
	if cleaned == "." {
		return workspaceMount, nil
	}
	if cleaned == ".." || strings.HasPrefix(cleaned, "../") {
		return "", fmt.Errorf("path escapes workspace: %s", rel)
	}
	return workspaceMount + "/" + cleaned, nil
}

func (e *DockerToolExecutor) toolNames() string {
	names := make([]string, 0, len(e.tools))
	for n := range e.tools {
		names = append(names, n)
	}
	return strings.Join(names, ", ")
}

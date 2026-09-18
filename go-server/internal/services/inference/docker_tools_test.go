package inference

import (
	"context"
	"encoding/json"
	"io"
	"strings"
	"testing"
)

type fakeDockerCall struct {
	called bool
	args   []string
	stdin  string
}

// newTestDockerExecutor returns an executor whose docker CLI is faked, so tests
// assert argv/stdin without a daemon.
func newTestDockerExecutor(t *testing.T, out string, runErr error) (*DockerToolExecutor, *fakeDockerCall) {
	t.Helper()
	call := &fakeDockerCall{}
	e := NewDockerToolExecutor(t.TempDir(), "alpine:test")
	e.run = func(_ context.Context, args []string, stdin io.Reader) ([]byte, error) {
		call.called = true
		call.args = args
		if stdin != nil {
			b, _ := io.ReadAll(stdin)
			call.stdin = string(b)
		}
		return []byte(out), runErr
	}
	return e, call
}

func argAfter(args []string, flag string) string {
	for i, a := range args {
		if a == flag && i+1 < len(args) {
			return args[i+1]
		}
	}
	return ""
}

func hasArg(args []string, want string) bool {
	for _, a := range args {
		if a == want {
			return true
		}
	}
	return false
}

func TestDockerContainerArgsIsolation(t *testing.T) {
	e, call := newTestDockerExecutor(t, "hello", nil)
	if _, err := e.Execute(context.Background(), "read_file", json.RawMessage(`{"path":"a.txt"}`)); err != nil {
		t.Fatalf("Execute: %v", err)
	}

	for _, want := range []string{
		"run", "--rm", "--network=none", "--read-only",
		"--cap-drop=ALL", "--security-opt=no-new-privileges",
		"--pids-limit=256", "--memory=512m", "--cpus=1",
	} {
		if !hasArg(call.args, want) {
			t.Errorf("missing isolation arg %q in %v", want, call.args)
		}
	}
	if got := argAfter(call.args, "-v"); got != e.workDir+":"+workspaceMount+":rw" {
		t.Errorf("volume = %q, want %s:/workspace:rw", got, e.workDir)
	}
	if got := argAfter(call.args, "-w"); got != workspaceMount {
		t.Errorf("workdir = %q, want %q", got, workspaceMount)
	}
	tail := call.args[len(call.args)-4:]
	want := []string{"alpine:test", "sh", "-c", `cat -- "$AF_TOOL_PATH"`}
	for i := range want {
		if tail[i] != want[i] {
			t.Fatalf("argv tail = %v, want %v", tail, want)
		}
	}
}

func TestDockerReadFile(t *testing.T) {
	e, call := newTestDockerExecutor(t, "file body", nil)
	raw, err := e.Execute(context.Background(), "read_file", json.RawMessage(`{"path":"docs/a.txt"}`))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if got := argAfter(call.args, "-e"); got != "AF_TOOL_PATH=/workspace/docs/a.txt" {
		t.Errorf("env = %q", got)
	}
	var res map[string]string
	if err := json.Unmarshal(raw, &res); err != nil || res["result"] != "file body" {
		t.Fatalf("result = %s (err %v)", raw, err)
	}
}

func TestDockerWriteFilePassesContentOnStdin(t *testing.T) {
	e, call := newTestDockerExecutor(t, "Wrote 5 bytes to /workspace/a.txt\n", nil)
	if _, err := e.Execute(context.Background(), "write_file", json.RawMessage(`{"path":"a.txt","content":"hello"}`)); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if call.stdin != "hello" {
		t.Errorf("stdin = %q, want %q", call.stdin, "hello")
	}
}

func TestDockerRunCommandUsesWorkdir(t *testing.T) {
	e, call := newTestDockerExecutor(t, "ok\n", nil)
	raw, err := e.Execute(context.Background(), "run_command", json.RawMessage(`{"command":"ls -la","workdir":"sub/dir"}`))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if got := argAfter(call.args, "-w"); got != "/workspace/sub/dir" {
		t.Errorf("workdir = %q, want /workspace/sub/dir", got)
	}
	if got := argAfter(call.args, "-e"); got != "AF_TOOL_CMD=ls -la" {
		t.Errorf("env = %q", got)
	}
	var res map[string]string
	if err := json.Unmarshal(raw, &res); err != nil || res["result"] != "ok" {
		t.Fatalf("result = %s (err %v)", raw, err)
	}
}

func TestDockerListFilesTrimsTrailingNewline(t *testing.T) {
	e, _ := newTestDockerExecutor(t, "a.txt\nb/\n", nil)
	raw, err := e.Execute(context.Background(), "list_files", json.RawMessage(`{"path":"."}`))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	var res map[string]string
	if err := json.Unmarshal(raw, &res); err != nil || res["result"] != "a.txt\nb/" {
		t.Fatalf("result = %s (err %v)", raw, err)
	}
}

func TestDockerRejectsWorkspaceEscapes(t *testing.T) {
	for _, path := range []string{"../secret", "a/../../secret", `C:\Windows\system32`} {
		e, call := newTestDockerExecutor(t, "", nil)
		raw, err := e.Execute(context.Background(), "read_file", json.RawMessage(`{"path":`+mustJSON(path)+`}`))
		if err != nil {
			t.Fatalf("Execute(%q): %v", path, err)
		}
		if call.called {
			t.Errorf("path %q should be rejected before docker runs", path)
		}
		var res map[string]string
		if err := json.Unmarshal(raw, &res); err != nil || !strings.Contains(res["error"], "escapes workspace") &&
			!strings.Contains(res["error"], "not allowed") {
			t.Errorf("path %q → %s, want escape error", path, raw)
		}
	}
}

func TestDockerRunFailureBecomesToolError(t *testing.T) {
	e, _ := newTestDockerExecutor(t, "docker: not found", errBoom{})
	raw, err := e.Execute(context.Background(), "run_command", json.RawMessage(`{"command":"true"}`))
	if err != nil {
		t.Fatalf("Execute should not return a Go error: %v", err)
	}
	var res map[string]string
	if err := json.Unmarshal(raw, &res); err != nil || !strings.Contains(res["error"], "docker run_command failed") {
		t.Fatalf("result = %s", raw)
	}
}

func TestDockerUnknownTool(t *testing.T) {
	e, _ := newTestDockerExecutor(t, "", nil)
	if _, err := e.Execute(context.Background(), "nope", json.RawMessage(`{}`)); err == nil {
		t.Fatal("unknown tool should return an error")
	}
}

func TestDockerCanExecute(t *testing.T) {
	e := NewDockerToolExecutor(t.TempDir(), "")
	for _, name := range []string{"read_file", "write_file", "run_command", "list_files"} {
		if !e.CanExecute(name) {
			t.Errorf("CanExecute(%q) = false", name)
		}
	}
	if e.CanExecute("get_weather") {
		t.Error("CanExecute(get_weather) = true, want false")
	}
}

type errBoom struct{}

func (errBoom) Error() string { return "boom" }

func mustJSON(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

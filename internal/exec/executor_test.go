package exec

import (
	"os"
	"os/exec"
	"testing"

	"agmux/internal/protocol"
)

func TestLocalExecSimple(t *testing.T) {
	e := NewExecutor(nil)

	result, err := e.ExecLocal("echo hello", 0, nil)
	if err != nil {
		t.Fatalf("ExecLocal failed: %v", err)
	}
	if result.Stdout != "hello\n" {
		t.Errorf("Stdout = %q, want \"hello\\n\"", result.Stdout)
	}
	if result.ExitCode != 0 {
		t.Errorf("ExitCode = %d, want 0", result.ExitCode)
	}
}

func TestLocalExecStderr(t *testing.T) {
	e := NewExecutor(nil)

	result, err := e.ExecLocal("echo err >&2", 0, nil)
	if err != nil {
		t.Fatalf("ExecLocal failed: %v", err)
	}
	if result.Stderr != "err\n" {
		t.Errorf("Stderr = %q, want \"err\\n\"", result.Stderr)
	}
}

func TestLocalExecExitCode(t *testing.T) {
	e := NewExecutor(nil)

	result, err := e.ExecLocal("exit 42", 0, nil)
	if err != nil {
		t.Fatalf("ExecLocal failed: %v", err)
	}
	if result.ExitCode != 42 {
		t.Errorf("ExitCode = %d, want 42", result.ExitCode)
	}
}

func TestLocalExecTimeout(t *testing.T) {
	e := NewExecutor(nil)

	result, err := e.ExecLocal("sleep 10", 2, nil)
	if err == nil {
		t.Fatal("expected timeout error")
	}
	if result.ExitCode != -1 {
		t.Errorf("ExitCode = %d, want -1 (timeout)", result.ExitCode)
	}
}

func TestLocalExecPipe(t *testing.T) {
	e := NewExecutor(nil)

	result, err := e.ExecLocal("echo hello | tr h H", 0, nil)
	if err != nil {
		t.Fatalf("ExecLocal pipe failed: %v", err)
	}
	if result.Stdout != "Hello\n" {
		t.Errorf("Stdout = %q, want \"Hello\\n\"", result.Stdout)
	}
}

func TestBuildSudoPrefix(t *testing.T) {
	tests := []struct {
		opts *protocol.SudoOptions
		want string
	}{
		{&protocol.SudoOptions{Login: true}, "sudo -i"},
		{&protocol.SudoOptions{User: "www-data"}, "sudo -u www-data"},
		{&protocol.SudoOptions{}, "sudo"},
	}

	for _, tt := range tests {
		got := buildSudoPrefix(tt.opts)
		if got != tt.want {
			t.Errorf("buildSudoPrefix() = %s, want %s", got, tt.want)
		}
	}
}

func TestCreateAskpassHelper(t *testing.T) {
	path, cleanup, err := createAskpassHelper("testpass123")
	if err != nil {
		t.Fatalf("createAskpassHelper failed: %v", err)
	}

	// Verify script outputs the password
	out, err := exec.Command(path).Output()
	if err != nil {
		t.Errorf("askpass script failed: %v", err)
	}
	if string(out) != "testpass123\n" {
		t.Errorf("askpass output = %q, want \"testpass123\\n\"", string(out))
	}

	cleanup()
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Error("askpass script not deleted after cleanup")
	}
}

func TestCreateAskpassHelperEmptyPassword(t *testing.T) {
	path, cleanup, err := createAskpassHelper("")
	if err != nil {
		t.Fatalf("createAskpassHelper with empty password failed: %v", err)
	}
	if path != "" {
		t.Errorf("expected empty path for empty password, got %s", path)
	}
	cleanup()
}
package exec

import (
	"io"
	"os"
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

func TestBuildSudoCommand(t *testing.T) {
	tests := []struct {
		name string
		opts *protocol.SudoOptions
		want string
	}{
		{name: "default", opts: &protocol.SudoOptions{}, want: "sudo -S -p '' /bin/sh -c \"echo hi\""},
		{name: "login", opts: &protocol.SudoOptions{Login: true}, want: "sudo -i -S -p '' /bin/sh -c \"echo hi\""},
		{name: "user", opts: &protocol.SudoOptions{User: "www-data"}, want: "sudo -u www-data -S -p '' /bin/sh -c \"echo hi\""},
	}

	for _, tt := range tests {
		if got := buildSudoCommand("/bin/sh -c \"echo hi\"", tt.opts); got != tt.want {
			t.Fatalf("%s: buildSudoCommand() = %q, want %q", tt.name, got, tt.want)
		}
	}
}

func TestWritePassword(t *testing.T) {
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe failed: %v", err)
	}

	done := make(chan []byte, 1)
	go func() {
		data, _ := io.ReadAll(reader)
		done <- data
	}()

	password := "pa'ss%word"
	if err := writePassword(writer, password); err != nil {
		t.Fatalf("writePassword failed: %v", err)
	}

	got := <-done
	want := password + "\n"
	if string(got) != want {
		t.Fatalf("writePassword() = %q, want %q", string(got), want)
	}
}

func TestWritePasswordEmptyPassword(t *testing.T) {
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe failed: %v", err)
	}

	done := make(chan []byte, 1)
	go func() {
		data, _ := io.ReadAll(reader)
		done <- data
	}()

	if err := writePassword(writer, ""); err != nil {
		t.Fatalf("writePassword failed: %v", err)
	}

	if got := <-done; len(got) != 0 {
		t.Fatalf("writePassword() wrote %q, want empty", string(got))
	}
}

func TestLocalExecLargeStderrDoesNotHang(t *testing.T) {
	e := NewExecutor(nil)

	result, err := e.ExecLocal("yes x | head -c 131072 >&2; echo ok", 5, nil)
	if err != nil {
		t.Fatalf("ExecLocal failed: %v", err)
	}
	if result.Stdout != "ok\n" {
		t.Fatalf("Stdout = %q, want %q", result.Stdout, "ok\n")
	}
	if len(result.Stderr) < 131072 {
		t.Fatalf("len(Stderr) = %d, want at least 131072", len(result.Stderr))
	}
	if result.ExitCode != 0 {
		t.Fatalf("ExitCode = %d, want 0", result.ExitCode)
	}
}

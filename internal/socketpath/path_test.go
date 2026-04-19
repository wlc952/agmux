package socketpath

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDefaultPrefersXDG(t *testing.T) {
	runtimeDir := t.TempDir()
	t.Setenv("XDG_RUNTIME_DIR", runtimeDir)
	if got, want := Default(), filepath.Join(runtimeDir, "agmux", "agmux.sock"); got != want {
		t.Fatalf("Default() = %s, want %s", got, want)
	}
}

func TestValidateRejectsNonSocketPath(t *testing.T) {
	path := filepath.Join(t.TempDir(), "not-a-socket")
	if err := os.WriteFile(path, []byte("x"), 0600); err != nil {
		t.Fatalf("WriteFile failed: %v", err)
	}

	err := Validate(path)
	if err == nil {
		t.Fatal("expected Validate to fail for non-socket path")
	}
	if !strings.Contains(err.Error(), "not a unix socket") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestRemoveIfOwnedSocketRejectsNonSocket(t *testing.T) {
	path := filepath.Join(t.TempDir(), "not-a-socket")
	if err := os.WriteFile(path, []byte("x"), 0600); err != nil {
		t.Fatalf("WriteFile failed: %v", err)
	}

	err := RemoveIfOwnedSocket(path)
	if err == nil {
		t.Fatal("expected RemoveIfOwnedSocket to fail for non-socket path")
	}
	if !strings.Contains(err.Error(), "not a unix socket") {
		t.Fatalf("unexpected error: %v", err)
	}
}

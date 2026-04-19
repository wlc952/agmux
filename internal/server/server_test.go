package server

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"agmux/internal/audit"
	"agmux/internal/protocol"
)

func TestStopIsIdempotent(t *testing.T) {
	homeDir := t.TempDir()
	t.Setenv("HOME", homeDir)

	srv := NewServer(filepath.Join(t.TempDir(), "agmux.sock"))

	if _, err := srv.sessions.ConnectLocal("local"); err != nil {
		t.Fatalf("ConnectLocal failed: %v", err)
	}

	if err := srv.Stop(); err != nil {
		t.Fatalf("first Stop failed: %v", err)
	}

	if err := srv.Stop(); err != nil {
		t.Fatalf("second Stop failed: %v", err)
	}
}

func TestLogExecAuditRecordsExitCode(t *testing.T) {
	homeDir := t.TempDir()
	t.Setenv("HOME", homeDir)

	srv := NewServer(filepath.Join(t.TempDir(), "agmux.sock"))
	defer srv.audit.Close()

	srv.logExecAudit("local", "exit 7", nil, &protocol.ExecResult{ExitCode: 7})

	entry := readLastAuditEntry(t, homeDir)
	if entry.Action != "exec" || entry.Result != "exit_7" {
		t.Fatalf("unexpected audit entry: %+v", entry)
	}
}

func TestLogTransferAuditRecordsBusinessFailure(t *testing.T) {
	homeDir := t.TempDir()
	t.Setenv("HOME", homeDir)

	srv := NewServer(filepath.Join(t.TempDir(), "agmux.sock"))
	defer srv.audit.Close()

	srv.logTransferAudit("prod", "scp", "a -> b", nil, &protocol.TransferResult{
		Success: false,
		Message: "partial failure",
	})

	entry := readLastAuditEntry(t, homeDir)
	if entry.Action != "scp" || entry.Result != "error" || entry.Detail != "partial failure" {
		t.Fatalf("unexpected audit entry: %+v", entry)
	}
}

func readLastAuditEntry(t *testing.T, homeDir string) audit.Entry {
	t.Helper()

	data, err := os.ReadFile(filepath.Join(homeDir, ".agmux", "audit.log"))
	if err != nil {
		t.Fatalf("read audit log failed: %v", err)
	}

	var entry audit.Entry
	if err := json.Unmarshal(data, &entry); err != nil {
		t.Fatalf("unmarshal audit entry failed: %v", err)
	}

	return entry
}

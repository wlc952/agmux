package transfer

import (
	"os"
	"path/filepath"
	"testing"
)

func TestEnsureLocalParentDirCreatesMissingDirectories(t *testing.T) {
	target := filepath.Join(t.TempDir(), "nested", "dir", "file.txt")

	if err := ensureLocalParentDir(target); err != nil {
		t.Fatalf("ensureLocalParentDir failed: %v", err)
	}

	if _, err := os.Stat(filepath.Dir(target)); err != nil {
		t.Fatalf("expected parent directory to exist: %v", err)
	}
}

func TestEnsureLocalParentDirAllowsCurrentDirectory(t *testing.T) {
	if err := ensureLocalParentDir("file.txt"); err != nil {
		t.Fatalf("ensureLocalParentDir failed: %v", err)
	}
}

func TestCheckPathTraversalRejectsEscapingPath(t *testing.T) {
	if err := checkPathTraversal("../etc/passwd"); err == nil {
		t.Fatal("expected path traversal error")
	}
}

func TestCheckPathTraversalAllowsLocalPath(t *testing.T) {
	if err := checkPathTraversal("nested/file.txt"); err != nil {
		t.Fatalf("unexpected path traversal error: %v", err)
	}
}

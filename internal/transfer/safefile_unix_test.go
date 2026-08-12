//go:build unix

package transfer

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSafeRootOpenFileRejectsSymlinkTarget(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "target.txt")
	if err := os.WriteFile(target, []byte("x"), 0600); err != nil {
		t.Fatalf("WriteFile failed: %v", err)
	}

	link := filepath.Join(dir, "link.txt")
	if err := os.Symlink(target, link); err != nil {
		t.Fatalf("Symlink failed: %v", err)
	}

	root, err := openSafeRoot(dir)
	if err != nil {
		t.Fatalf("openSafeRoot failed: %v", err)
	}
	defer root.Close()

	f, err := root.openFile("link.txt", 0600)
	if err == nil {
		f.Close()
		t.Fatal("expected no-follow open to reject symlink target")
	}
}

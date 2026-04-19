package transfer

import (
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/unix"
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

func TestSafeLocalDownloadPathRejectsSymlinkEscape(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()

	if err := os.MkdirAll(filepath.Join(root, "nested"), 0755); err != nil {
		t.Fatalf("MkdirAll failed: %v", err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "nested", "escape")); err != nil {
		t.Fatalf("Symlink failed: %v", err)
	}

	rootResolved, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatalf("EvalSymlinks failed: %v", err)
	}

	target := filepath.Join(root, "nested", "escape", "file.txt")
	if _, err := safeLocalDownloadPath(root, rootResolved, target); err == nil {
		t.Fatal("expected symlink escape error")
	}
}

func TestSafeLocalDownloadPathAllowsRegularPath(t *testing.T) {
	root := t.TempDir()
	rootResolved, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatalf("EvalSymlinks failed: %v", err)
	}

	target := filepath.Join(root, "nested", "file.txt")
	safe, err := safeLocalDownloadPath(root, rootResolved, target)
	if err != nil {
		t.Fatalf("safeLocalDownloadPath failed: %v", err)
	}
	if safe != target {
		t.Fatalf("safeLocalDownloadPath = %s, want %s", safe, target)
	}
}

func TestOpenLocalFileNoFollowRejectsSymlinkTarget(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "target.txt")
	if err := os.WriteFile(target, []byte("x"), 0600); err != nil {
		t.Fatalf("WriteFile failed: %v", err)
	}

	link := filepath.Join(dir, "link.txt")
	if err := os.Symlink(target, link); err != nil {
		t.Fatalf("Symlink failed: %v", err)
	}

	rootFD, err := openRootDirNoFollow(dir)
	if err != nil {
		t.Fatalf("openRootDirNoFollow failed: %v", err)
	}
	defer unix.Close(rootFD)

	f, err := openLocalFileNoFollowAt(rootFD, "link.txt", 0600)
	if err == nil {
		f.Close()
		t.Fatal("expected no-follow open to reject symlink target")
	}
}

func TestOpenLocalFileNoFollowCreatesRegularFile(t *testing.T) {
	dir := t.TempDir()
	rootFD, err := openRootDirNoFollow(dir)
	if err != nil {
		t.Fatalf("openRootDirNoFollow failed: %v", err)
	}
	defer unix.Close(rootFD)

	f, err := openLocalFileNoFollowAt(rootFD, "safe.txt", 0600)
	if err != nil {
		t.Fatalf("openLocalFileNoFollowAt failed: %v", err)
	}
	defer f.Close()
}

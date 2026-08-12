//go:build !unix

package transfer

import (
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// safeRoot is the non-unix fallback: plain path-based file creation.
// Path traversal ("..") is still rejected, but O_NOFOLLOW fd anchoring —
// protection against symlink-swap races during download — is a unix-only
// hardening and is not available on this platform.
type safeRoot struct {
	path string
}

func openSafeRoot(path string) (*safeRoot, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, fmt.Errorf("failed to open destination root: %w", err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("destination root is not a directory: %s", path)
	}
	return &safeRoot{path: path}, nil
}

// Close is a no-op on non-unix platforms (no descriptor is held).
func (r *safeRoot) Close() {}

// openFile creates (or truncates) relPath under the root, creating
// intermediate directories as needed.
func (r *safeRoot) openFile(relPath string, perm os.FileMode) (*os.File, error) {
	cleanRel, err := cleanRelPath(relPath)
	if err != nil {
		return nil, err
	}
	if cleanRel == "" {
		return nil, fmt.Errorf("invalid destination relative path: %s", relPath)
	}

	full := filepath.Join(r.path, cleanRel)
	if err := os.MkdirAll(filepath.Dir(full), 0755); err != nil {
		return nil, fmt.Errorf("failed to create destination directory: %w", err)
	}
	return os.OpenFile(full, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, perm.Perm())
}

// ensureDir creates relPath (and any missing parents) under the root.
func (r *safeRoot) ensureDir(relPath string, mode os.FileMode) error {
	cleanRel, err := cleanRelPath(relPath)
	if err != nil {
		return err
	}
	if cleanRel == "" {
		return nil
	}
	return os.MkdirAll(filepath.Join(r.path, cleanRel), mode.Perm())
}

func setFileTimes(file *os.File, modTime time.Time) error {
	return os.Chtimes(file.Name(), modTime, modTime)
}

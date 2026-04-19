package socketpath

import (
	"fmt"
	"os"
	"path/filepath"
)

// Default returns a per-user socket path.
func Default() string {
	if runtimeDir := os.Getenv("XDG_RUNTIME_DIR"); runtimeDir != "" {
		return filepath.Join(runtimeDir, "agmux", "agmux.sock")
	}

	if homeDir, err := os.UserHomeDir(); err == nil && homeDir != "" {
		return filepath.Join(homeDir, ".agmux", "run", "agmux.sock")
	}

	return filepath.Join(os.TempDir(), fmt.Sprintf("agmux-%d.sock", os.Getuid()))
}

// EnsureParentDir ensures the socket parent directory exists.
func EnsureParentDir(socketPath string) error {
	dir := filepath.Dir(socketPath)
	if info, err := os.Stat(dir); err == nil {
		if !info.IsDir() {
			return fmt.Errorf("socket parent path is not a directory: %s", dir)
		}
		return nil
	} else if !os.IsNotExist(err) {
		return err
	}

	return os.MkdirAll(dir, 0700)
}

// Validate ensures the socket file is a Unix socket owned by the current user and not group/world accessible.
func Validate(socketPath string) error {
	info, err := os.Lstat(socketPath)
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSocket == 0 {
		return fmt.Errorf("refusing to use %s: not a unix socket", socketPath)
	}
	if info.Mode().Perm()&0077 != 0 {
		return fmt.Errorf("refusing to use %s: permissions are too broad (%#o)", socketPath, info.Mode().Perm())
	}

	uid, err := fileOwnerUID(info)
	if err != nil {
		return fmt.Errorf("failed to inspect owner for %s: %w", socketPath, err)
	}
	if uid != currentUID() {
		return fmt.Errorf("refusing to use %s: owned by uid %d, expected %d", socketPath, uid, currentUID())
	}
	return nil
}

// RemoveIfOwnedSocket removes an existing socket file when it is a socket owned by the current user.
func RemoveIfOwnedSocket(socketPath string) error {
	info, err := os.Lstat(socketPath)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSocket == 0 {
		return fmt.Errorf("refusing to remove %s: existing path is not a unix socket", socketPath)
	}

	uid, err := fileOwnerUID(info)
	if err != nil {
		return fmt.Errorf("failed to inspect owner for %s: %w", socketPath, err)
	}
	if uid != currentUID() {
		return fmt.Errorf("refusing to remove %s: owned by uid %d, expected %d", socketPath, uid, currentUID())
	}
	return os.Remove(socketPath)
}

//go:build unix

package transfer

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/sys/unix"
)

// safeRoot anchors download file creation to an open directory file
// descriptor. All traversal happens relative to that fd with O_NOFOLLOW,
// so a hostile or raced symlink swap cannot redirect writes outside the
// destination root (TOCTOU protection).
type safeRoot struct {
	fd int
}

func openSafeRoot(path string) (*safeRoot, error) {
	dirFD, err := unix.Open(path, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_DIRECTORY|unix.O_NOFOLLOW, 0)
	if err != nil {
		return nil, fmt.Errorf("failed to open destination root safely: %w", err)
	}
	return &safeRoot{fd: dirFD}, nil
}

// Close releases the root directory descriptor.
func (r *safeRoot) Close() {
	unix.Close(r.fd)
}

// openFile creates (or truncates) relPath under the root without following
// symlinks, creating intermediate directories as needed.
func (r *safeRoot) openFile(relPath string, perm os.FileMode) (*os.File, error) {
	cleanRel, err := cleanRelPath(relPath)
	if err != nil {
		return nil, err
	}
	if cleanRel == "" {
		return nil, fmt.Errorf("invalid destination relative path: %s", relPath)
	}

	if err := r.ensureDir(filepath.Dir(cleanRel), 0755); err != nil {
		return nil, err
	}

	parentFD, err := r.openDir(filepath.Dir(cleanRel))
	if err != nil {
		return nil, err
	}
	if parentFD != r.fd {
		defer unix.Close(parentFD)
	}

	name := filepath.Base(cleanRel)
	fileFD, err := unix.Openat(parentFD, name, unix.O_CREAT|unix.O_WRONLY|unix.O_TRUNC|unix.O_CLOEXEC|unix.O_NOFOLLOW, uint32(perm.Perm()))
	if err != nil {
		return nil, fmt.Errorf("failed to open destination file safely: %w", err)
	}

	file := os.NewFile(uintptr(fileFD), cleanRel)
	if file == nil {
		unix.Close(fileFD)
		return nil, fmt.Errorf("failed to wrap destination file descriptor")
	}
	return file, nil
}

// ensureDir creates relPath (and any missing parents) under the root,
// refusing to follow symlinks.
func (r *safeRoot) ensureDir(relPath string, mode os.FileMode) error {
	cleanRel, err := cleanRelPath(relPath)
	if err != nil {
		return err
	}
	if cleanRel == "" {
		return nil
	}

	parts := strings.Split(cleanRel, string(os.PathSeparator))
	currentFD := r.fd
	for idx, part := range parts {
		if part == "" || part == "." {
			continue
		}

		createMode := uint32(0755)
		if idx == len(parts)-1 {
			createMode = uint32(mode.Perm())
		}
		if err := unix.Mkdirat(currentFD, part, createMode); err != nil && !errors.Is(err, unix.EEXIST) {
			if currentFD != r.fd {
				unix.Close(currentFD)
			}
			return fmt.Errorf("failed to create destination directory: %w", err)
		}

		nextFD, err := unix.Openat(currentFD, part, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_DIRECTORY|unix.O_NOFOLLOW, 0)
		if currentFD != r.fd {
			unix.Close(currentFD)
		}
		if err != nil {
			return fmt.Errorf("failed to open destination directory safely: %w", err)
		}
		currentFD = nextFD
	}
	if currentFD != r.fd {
		unix.Close(currentFD)
	}
	return nil
}

// openDir opens relPath under the root without following symlinks.
// Returns the root fd itself for the root directory (caller must not close it).
func (r *safeRoot) openDir(relPath string) (int, error) {
	cleanRel, err := cleanRelPath(relPath)
	if err != nil {
		return -1, err
	}
	if cleanRel == "" {
		return r.fd, nil
	}

	parts := strings.Split(cleanRel, string(os.PathSeparator))
	currentFD := r.fd
	for _, part := range parts {
		if part == "" || part == "." {
			continue
		}

		nextFD, err := unix.Openat(currentFD, part, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_DIRECTORY|unix.O_NOFOLLOW, 0)
		if currentFD != r.fd {
			unix.Close(currentFD)
		}
		if err != nil {
			return -1, fmt.Errorf("failed to open destination directory safely: %w", err)
		}
		currentFD = nextFD
	}
	return currentFD, nil
}

func setFileTimes(file *os.File, modTime time.Time) error {
	tv := unix.NsecToTimeval(modTime.UnixNano())
	return unix.Futimes(int(file.Fd()), []unix.Timeval{tv, tv})
}

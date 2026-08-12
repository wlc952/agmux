package transfer

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"gssh/internal/protocol"
	"gssh/internal/session"

	"github.com/pkg/sftp"
)

// Service handles file transfer operations via SFTP.
type Service struct {
	sessions *session.Manager
}

// NewService creates a new transfer service.
func NewService(sessions *session.Manager) *Service {
	return &Service{sessions: sessions}
}

// Upload transfers files from local to remote.
func (s *Service) Upload(sessionName, localPath, remotePath string) (*protocol.TransferResult, error) {
	sess, err := s.getSFTPSession(sessionName)
	if err != nil {
		return nil, err
	}
	defer sess.Close()

	start := time.Now()
	bytes, err := sess.Upload(localPath, remotePath)
	duration := time.Since(start).Milliseconds()

	if err != nil {
		return &protocol.TransferResult{
			Success:  false,
			Message:  err.Error(),
			Bytes:    bytes,
			Duration: duration,
		}, nil
	}

	return &protocol.TransferResult{
		Success:  true,
		Message:  fmt.Sprintf("Transferred %d bytes in %dms", bytes, duration),
		Bytes:    bytes,
		Duration: duration,
	}, nil
}

// Download transfers files from remote to local.
func (s *Service) Download(sessionName, remotePath, localPath string) (*protocol.TransferResult, error) {
	sess, err := s.getSFTPSession(sessionName)
	if err != nil {
		return nil, err
	}
	defer sess.Close()

	start := time.Now()
	bytes, err := sess.Download(remotePath, localPath)
	duration := time.Since(start).Milliseconds()

	if err != nil {
		return &protocol.TransferResult{
			Success:  false,
			Message:  err.Error(),
			Bytes:    bytes,
			Duration: duration,
		}, nil
	}

	return &protocol.TransferResult{
		Success:  true,
		Message:  fmt.Sprintf("Transferred %d bytes in %dms", bytes, duration),
		Bytes:    bytes,
		Duration: duration,
	}, nil
}

// ListDir lists files in a remote directory.
func (s *Service) ListDir(sessionName, path string) ([]string, error) {
	sess, err := s.getSFTPSession(sessionName)
	if err != nil {
		return nil, err
	}
	defer sess.Close()

	return sess.List(path)
}

// Mkdir creates a remote directory.
func (s *Service) Mkdir(sessionName, path string) error {
	sess, err := s.getSFTPSession(sessionName)
	if err != nil {
		return err
	}
	defer sess.Close()

	return sess.Mkdir(path)
}

// Remove removes a remote file.
func (s *Service) Remove(sessionName, path string) error {
	sess, err := s.getSFTPSession(sessionName)
	if err != nil {
		return err
	}
	defer sess.Close()

	return sess.Remove(path)
}

// --- SFTP Client wrapper ---

type sftpSession struct {
	client *sftp.Client
}

func (s *Service) getSFTPSession(sessionName string) (*sftpSession, error) {
	sess, err := s.sessions.Get(sessionName)
	if err != nil {
		return nil, err
	}

	if sess.IsLocal() {
		return nil, fmt.Errorf("SFTP not available for local sessions")
	}

	sshSess := sess.(*session.SSHSession)
	sshClient := sshSess.GetSSHClient()
	if sshClient == nil {
		return nil, fmt.Errorf("session not connected")
	}

	sftpClient, err := sftp.NewClient(sshClient)
	if err != nil {
		return nil, fmt.Errorf("failed to create SFTP client: %w", err)
	}

	return &sftpSession{client: sftpClient}, nil
}

func (s *sftpSession) Close() error {
	return s.client.Close()
}

func (s *sftpSession) Upload(localPath, remotePath string) (int64, error) {
	localInfo, err := os.Stat(localPath)
	if err != nil {
		return 0, fmt.Errorf("failed to stat local path: %w", err)
	}

	if !localInfo.IsDir() {
		return s.uploadFile(localPath, remotePath)
	}

	var totalWritten int64
	err = filepath.Walk(localPath, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}

		relPath, err := filepath.Rel(localPath, path)
		if err != nil {
			return err
		}

		targetPath := filepath.ToSlash(filepath.Join(remotePath, relPath))

		if info.IsDir() {
			if err := s.client.MkdirAll(targetPath); err != nil {
				return fmt.Errorf("failed to create remote directory %s: %w", targetPath, err)
			}
			return nil
		}

		// Skip if same size+mtime
		remoteInfo, err := s.client.Stat(targetPath)
		if err == nil && remoteInfo.Size() == info.Size() && remoteInfo.ModTime().Unix() == info.ModTime().Unix() {
			return nil
		}

		written, err := s.uploadFile(path, targetPath)
		if err != nil {
			return err
		}
		totalWritten += written
		if err := s.client.Chtimes(targetPath, info.ModTime(), info.ModTime()); err != nil {
			return fmt.Errorf("failed to preserve remote timestamps for %s: %w", targetPath, err)
		}
		return nil
	})

	return totalWritten, err
}

func (s *sftpSession) uploadFile(localPath, remotePath string) (int64, error) {
	localFile, err := os.Open(localPath)
	if err != nil {
		return 0, fmt.Errorf("failed to open local file: %w", err)
	}
	defer localFile.Close()

	localInfo, err := localFile.Stat()
	if err != nil {
		return 0, fmt.Errorf("failed to stat local file: %w", err)
	}

	if err := s.client.MkdirAll(filepath.ToSlash(filepath.Dir(remotePath))); err != nil {
		return 0, fmt.Errorf("failed to create remote parent directory: %w", err)
	}

	remoteFile, err := s.client.Create(remotePath)
	if err != nil {
		return 0, fmt.Errorf("failed to create remote file: %w", err)
	}
	defer remoteFile.Close()

	written, err := io.Copy(remoteFile, localFile)
	if err != nil {
		return 0, fmt.Errorf("failed to upload file: %w", err)
	}

	if err := s.client.Chmod(remotePath, localInfo.Mode()); err != nil {
		return 0, fmt.Errorf("failed to preserve remote mode: %w", err)
	}
	return written, nil
}

func (s *sftpSession) Download(remotePath, localPath string) (int64, error) {
	remoteInfo, err := s.client.Stat(remotePath)
	if err != nil {
		return 0, fmt.Errorf("failed to stat remote path: %w", err)
	}

	if !remoteInfo.IsDir() {
		targetPath, rootAbs, rootResolved, err := prepareSingleFileDownloadTarget(localPath)
		if err != nil {
			return 0, err
		}
		safeTarget, err := safeLocalDownloadPath(rootAbs, rootResolved, targetPath)
		if err != nil {
			return 0, err
		}
		root, err := openSafeRoot(rootAbs)
		if err != nil {
			return 0, err
		}
		defer root.Close()

		return s.downloadFile(remotePath, root, filepath.Base(safeTarget), remoteInfo.ModTime())
	}

	rootAbs, rootResolved, err := prepareDownloadRoot(localPath)
	if err != nil {
		return 0, err
	}
	root, err := openSafeRoot(rootAbs)
	if err != nil {
		return 0, err
	}
	defer root.Close()

	var totalWritten int64
	walker := s.client.Walk(remotePath)
	for walker.Step() {
		if walker.Err() != nil {
			return totalWritten, walker.Err()
		}

		path := walker.Path()
		info := walker.Stat()

		relPath, err := filepath.Rel(remotePath, path)
		if err != nil {
			return totalWritten, err
		}

		if err := checkPathTraversal(relPath); err != nil {
			return totalWritten, err
		}
		if relPath == "." {
			continue
		}

		targetPath := filepath.Join(rootAbs, relPath)
		safeTarget, err := safeLocalDownloadPath(rootAbs, rootResolved, targetPath)
		if err != nil {
			return totalWritten, err
		}

		if info.IsDir() {
			if err := root.ensureDir(relPath, info.Mode()); err != nil {
				return totalWritten, fmt.Errorf("failed to create local directory %s: %w", safeTarget, err)
			}
			if err := ensureSafeLocalDir(safeTarget, info.Mode()); err != nil {
				return totalWritten, fmt.Errorf("failed to create local directory %s: %w", safeTarget, err)
			}
			continue
		}

		// Skip if same size+mtime
		localFileInfo, err := os.Stat(safeTarget)
		if err == nil && localFileInfo.Size() == info.Size() && localFileInfo.ModTime().Unix() == info.ModTime().Unix() {
			continue
		}

		written, err := s.downloadFile(path, root, relPath, info.ModTime())
		if err != nil {
			return totalWritten, err
		}
		totalWritten += written
	}

	return totalWritten, nil
}

func (s *sftpSession) downloadFile(remotePath string, root *safeRoot, relPath string, modTime time.Time) (int64, error) {
	remoteFile, err := s.client.Open(remotePath)
	if err != nil {
		return 0, fmt.Errorf("failed to open remote file: %w", err)
	}
	defer remoteFile.Close()

	localFile, err := root.openFile(relPath, 0600)
	if err != nil {
		return 0, fmt.Errorf("failed to create local file: %w", err)
	}
	defer localFile.Close()

	written, err := io.Copy(localFile, remoteFile)
	if err != nil {
		return 0, fmt.Errorf("failed to download file: %w", err)
	}

	remoteInfo, err := s.client.Stat(remotePath)
	if err == nil {
		if chmodErr := localFile.Chmod(remoteInfo.Mode()); chmodErr != nil {
			return 0, fmt.Errorf("failed to preserve local mode: %w", chmodErr)
		}
	}
	if err := setFileTimes(localFile, modTime); err != nil {
		return 0, fmt.Errorf("failed to preserve local timestamps: %w", err)
	}

	return written, nil
}

func (s *sftpSession) List(path string) ([]string, error) {
	entries, err := s.client.ReadDir(path)
	if err != nil {
		return nil, fmt.Errorf("failed to read directory: %w", err)
	}

	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		names = append(names, entry.Name())
	}
	return names, nil
}

func (s *sftpSession) Mkdir(path string) error {
	return s.client.Mkdir(path)
}

func (s *sftpSession) Remove(path string) error {
	return s.client.Remove(path)
}

func checkPathTraversal(relPath string) error {
	if !filepath.IsLocal(relPath) {
		return fmt.Errorf("path traversal detected: %s escapes destination", relPath)
	}
	return nil
}

func prepareDownloadRoot(localPath string) (string, string, error) {
	rootAbs, err := filepath.Abs(localPath)
	if err != nil {
		return "", "", fmt.Errorf("failed to resolve destination path: %w", err)
	}
	if err := ensureSafeLocalDir(rootAbs, 0755); err != nil {
		return "", "", err
	}
	rootResolved, err := filepath.EvalSymlinks(rootAbs)
	if err != nil {
		return "", "", fmt.Errorf("failed to resolve destination path: %w", err)
	}
	return rootAbs, rootResolved, nil
}

func prepareSingleFileDownloadTarget(localPath string) (string, string, string, error) {
	targetAbs, err := filepath.Abs(localPath)
	if err != nil {
		return "", "", "", fmt.Errorf("failed to resolve destination path: %w", err)
	}
	rootAbs := filepath.Dir(targetAbs)
	if err := ensureSafeLocalDir(rootAbs, 0755); err != nil {
		return "", "", "", err
	}
	rootResolved, err := filepath.EvalSymlinks(rootAbs)
	if err != nil {
		return "", "", "", fmt.Errorf("failed to resolve destination path: %w", err)
	}
	return targetAbs, rootAbs, rootResolved, nil
}

func safeLocalDownloadPath(rootAbs, rootResolved, targetPath string) (string, error) {
	targetAbs, err := filepath.Abs(targetPath)
	if err != nil {
		return "", fmt.Errorf("failed to resolve destination path: %w", err)
	}

	if pathEscapesRoot(rootAbs, targetAbs) {
		return "", fmt.Errorf("path traversal detected: %s escapes destination", targetAbs)
	}

	resolvedParent, err := resolveExistingParent(filepath.Dir(targetAbs))
	if err != nil {
		return "", fmt.Errorf("failed to resolve destination parent: %w", err)
	}
	if pathEscapesRoot(rootResolved, resolvedParent) {
		return "", fmt.Errorf("symlink traversal detected: %s escapes destination", targetAbs)
	}

	if info, err := os.Lstat(targetAbs); err == nil && info.Mode()&os.ModeSymlink != 0 {
		return "", fmt.Errorf("symlink traversal detected: %s is a symlink", targetAbs)
	} else if err != nil && !os.IsNotExist(err) {
		return "", fmt.Errorf("failed to inspect destination path: %w", err)
	}

	return targetAbs, nil
}

func resolveExistingParent(path string) (string, error) {
	current := path
	for {
		resolved, err := filepath.EvalSymlinks(current)
		if err == nil {
			return resolved, nil
		}
		if !os.IsNotExist(err) {
			return "", err
		}
		next := filepath.Dir(current)
		if next == current {
			return "", err
		}
		current = next
	}
}

func ensureSafeLocalDir(path string, mode os.FileMode) error {
	info, err := os.Lstat(path)
	if err == nil {
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("destination path is a symlink: %s", path)
		}
		if !info.IsDir() {
			return fmt.Errorf("destination path is not a directory: %s", path)
		}
		return nil
	}
	if !os.IsNotExist(err) {
		return fmt.Errorf("failed to inspect destination path %s: %w", path, err)
	}
	if err := os.MkdirAll(path, mode); err != nil {
		return fmt.Errorf("failed to create destination directory %s: %w", path, err)
	}
	info, err = os.Lstat(path)
	if err != nil {
		return fmt.Errorf("failed to inspect destination path %s: %w", path, err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("destination path is a symlink: %s", path)
	}
	if !info.IsDir() {
		return fmt.Errorf("destination path is not a directory: %s", path)
	}
	return nil
}

func pathEscapesRoot(root, target string) bool {
	rel, err := filepath.Rel(root, target)
	if err != nil {
		return true
	}
	return rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator))
}

func ensureLocalParentDir(localPath string) error {
	parent := filepath.Dir(localPath)
	if parent == "." {
		return nil
	}
	if err := os.MkdirAll(parent, 0755); err != nil {
		return fmt.Errorf("failed to create local parent directory %s: %w", parent, err)
	}
	return nil
}

// cleanRelPath cleans a root-relative path and rejects traversal above the
// root. Returns "" for the root itself ("." or empty input). Shared by the
// unix and non-unix safeRoot implementations.
func cleanRelPath(relPath string) (string, error) {
	clean := filepath.Clean(relPath)
	if clean == "." || clean == "" {
		return "", nil
	}
	if clean == ".." || strings.HasPrefix(clean, ".."+string(os.PathSeparator)) {
		return "", fmt.Errorf("invalid destination relative path: %s", relPath)
	}
	return clean, nil
}

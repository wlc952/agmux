package audit

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Entry represents an audit log entry.
type Entry struct {
	Timestamp time.Time `json:"timestamp"`
	Session   string    `json:"session,omitempty"`
	Action    string    `json:"action"` // connect, detach, kill, exec, forward, etc.
	Command   string    `json:"cmd,omitempty"`
	Result    string    `json:"result"` // success, error, timeout
	Detail    string    `json:"detail,omitempty"`
}

// Logger writes audit entries to ~/.gssh/audit.log.
type Logger struct {
	file *os.File
	mu   sync.Mutex
}

// NewLogger creates an audit logger.
func NewLogger() *Logger {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to resolve audit log home dir: %v\n", err)
		return &Logger{}
	}

	logger, err := NewLoggerAt(filepath.Join(homeDir, ".gssh", "audit.log"))
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to open audit log: %v\n", err)
		return &Logger{}
	}

	return logger
}

// NewLoggerAt creates an audit logger at an explicit path.
func NewLoggerAt(path string) (*Logger, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return nil, fmt.Errorf("failed to create audit log directory: %w", err)
	}

	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		return nil, fmt.Errorf("failed to open audit log file: %w", err)
	}

	return &Logger{file: f}, nil
}

// Log writes an audit entry as one JSON line.
func (l *Logger) Log(entry Entry) error {
	l.mu.Lock()
	defer l.mu.Unlock()

	if l.file == nil {
		return nil
	}

	entry.Timestamp = time.Now()
	data, err := json.Marshal(entry)
	if err != nil {
		return err
	}

	_, err = l.file.Write(append(data, '\n'))
	return err
}

// Close closes the audit log file.
func (l *Logger) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()

	if l.file != nil {
		err := l.file.Close()
		l.file = nil
		return err
	}
	return nil
}

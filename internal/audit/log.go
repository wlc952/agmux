package audit

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
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

// Logger writes audit entries to ~/.agmux/audit.log.
type Logger struct {
	file *os.File
}

// NewLogger creates an audit logger.
func NewLogger() *Logger {
	homeDir, _ := os.UserHomeDir()
	dir := filepath.Join(homeDir, ".agmux")
	os.MkdirAll(dir, 0700)

	f, err := os.OpenFile(filepath.Join(dir, "audit.log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to open audit log: %v\n", err)
		return &Logger{}
	}

	return &Logger{file: f}
}

// Log writes an audit entry as one JSON line.
func (l *Logger) Log(entry Entry) error {
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
	if l.file != nil {
		return l.file.Close()
	}
	return nil
}
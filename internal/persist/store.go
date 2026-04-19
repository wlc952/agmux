package persist

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"agmux/internal/session"
)

// Store persists session metadata to disk for daemon restart recovery.
type Store struct {
	path string
}

// SessionState is the serializable representation of a session.
type SessionState struct {
	Name      string `json:"name"`
	Type      string `json:"type"` // "ssh" or "local"
	Host      string `json:"host"`
	User      string `json:"user"`
	Port      int    `json:"port,omitempty"`
	KeyPath   string `json:"key_path,omitempty"`
	Status    string `json:"status"`
	CreatedAt int64  `json:"created_at"` // Unix timestamp
}

// NewStore creates a persistence store at ~/.agmux/state.json.
func NewStore() *Store {
	homeDir, _ := os.UserHomeDir()
	dir := filepath.Join(homeDir, ".agmux")
	os.MkdirAll(dir, 0700)
	return &Store{path: filepath.Join(dir, "state.json")}
}

// Save writes session state to disk.
func (s *Store) Save(sessions []SessionState) error {
	data, err := json.Marshal(sessions)
	if err != nil {
		return fmt.Errorf("failed to marshal state: %w", err)
	}
	return os.WriteFile(s.path, data, 0600)
}

// Load reads session state from disk.
func (s *Store) Load() ([]SessionState, error) {
	data, err := os.ReadFile(s.path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil // no state file = fresh start
		}
		return nil, fmt.Errorf("failed to read state: %w", err)
	}

	var states []SessionState
	if err := json.Unmarshal(data, &states); err != nil {
		return nil, fmt.Errorf("failed to unmarshal state: %w", err)
	}
	return states, nil
}

// SessionToState converts a session to its persistable state.
func SessionToState(sess session.Session) SessionState {
	state := SessionState{
		Name:      sess.GetName(),
		Type:      sess.GetType(),
		Host:      sess.GetHost(),
		User:      sess.GetUser(),
		Status:    string(sess.GetStatus()),
		CreatedAt: sess.GetCreatedAt().Unix(),
	}

	if !sess.IsLocal() {
		sshSess := sess.(*session.SSHSession)
		state.Port = sshSess.Port
		state.KeyPath = sshSess.GetKeyPath()
	}

	return state
}

// CollectState collects state from all active sessions.
func CollectState(sessions []session.Session) []SessionState {
	states := make([]SessionState, len(sessions))
	for i, s := range sessions {
		states[i] = SessionToState(s)
	}
	return states
}

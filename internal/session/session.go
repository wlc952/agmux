package session

import (
	"fmt"
	"os/user"
	"sync"
	"time"

	agssh "agmux/internal/ssh"
	"golang.org/x/crypto/ssh"
)

// Status represents a session's lifecycle state.
type Status string

const (
	StatusConnected    Status = "connected"
	StatusDisconnected Status = "disconnected"
	StatusReconnecting Status = "reconnecting"
	StatusOffline      Status = "offline"
	StatusConnecting   Status = "connecting"
)

// Session is the common interface for both SSH and local sessions.
type Session interface {
	GetName() string
	GetType() string // "ssh" or "local"
	GetHost() string
	GetUser() string
	GetStatus() Status
	SetStatus(Status)
	Close() error
	IsLocal() bool
	GetCreatedAt() time.Time
	GetLastCmd() string
	SetLastCmd(cmd string)
}

// SSHSession represents a remote SSH session.
type SSHSession struct {
	Name      string
	Host      string
	User      string
	Port      int
	Password  string // in-memory only, not persisted
	KeyPath   string
	Status    Status
	Client    *agssh.Client
	CreatedAt time.Time
	LastCmd   string
	mu        sync.RWMutex
}

func (s *SSHSession) GetName() string         { return s.Name }
func (s *SSHSession) GetType() string         { return "ssh" }
func (s *SSHSession) GetHost() string         { return s.Host }
func (s *SSHSession) GetUser() string         { return s.User }
func (s *SSHSession) IsLocal() bool           { return false }
func (s *SSHSession) GetCreatedAt() time.Time { return s.CreatedAt }

func (s *SSHSession) GetStatus() Status {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.Status
}

func (s *SSHSession) SetStatus(st Status) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.Status = st
}

func (s *SSHSession) GetLastCmd() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.LastCmd
}

func (s *SSHSession) SetLastCmd(cmd string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.LastCmd = cmd
}

func (s *SSHSession) Close() error {
	s.mu.Lock()
	client := s.Client
	s.Client = nil
	s.Status = StatusDisconnected
	s.mu.Unlock()
	if client != nil {
		return client.Close()
	}
	return nil
}

func (s *SSHSession) GetSSHClient() *ssh.Client {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.Client == nil {
		return nil
	}
	return s.Client.GoClient
}

func (s *SSHSession) GetPassword() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.Password
}

func (s *SSHSession) GetKeyPath() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.KeyPath
}

func (s *SSHSession) SetPassword(p string) {
	s.mu.Lock()
	s.Password = p
	s.mu.Unlock()
}

func (s *SSHSession) SetKeyPath(k string) {
	s.mu.Lock()
	s.KeyPath = k
	s.mu.Unlock()
}

func (s *SSHSession) GetClient() *agssh.Client {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.Client
}

// SetClient sets the SSH client (for reconnect monitor).
func (s *SSHSession) SetClient(client *agssh.Client) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.Client = client
}

// LocalSession represents a local execution session.
type LocalSession struct {
	Name      string
	Host      string // always "local"
	User      string // current OS user
	Status    Status // always "connected"
	CreatedAt time.Time
	LastCmd   string
	mu        sync.RWMutex
}

func (s *LocalSession) GetName() string         { return s.Name }
func (s *LocalSession) GetType() string         { return "local" }
func (s *LocalSession) GetHost() string         { return s.Host }
func (s *LocalSession) GetUser() string         { return s.User }
func (s *LocalSession) IsLocal() bool           { return true }
func (s *LocalSession) GetCreatedAt() time.Time { return s.CreatedAt }

func (s *LocalSession) GetStatus() Status {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.Status
}

func (s *LocalSession) SetStatus(st Status) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.Status = st
}

func (s *LocalSession) GetLastCmd() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.LastCmd
}

func (s *LocalSession) SetLastCmd(cmd string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.LastCmd = cmd
}

func (s *LocalSession) Close() error {
	s.mu.Lock()
	s.Status = StatusDisconnected
	s.mu.Unlock()
	return nil // local sessions have nothing to close
}

// --- Manager ---

// Manager manages named sessions (both SSH and local).
type Manager struct {
	sessions    map[string]Session // keyed by Name
	defaultName string             // default session name
	mu          sync.RWMutex
}

// NewManager creates a new session manager.
func NewManager() *Manager {
	return &Manager{
		sessions: make(map[string]Session),
	}
}

// ConnectSSH creates a new SSH session or returns an existing one.
func (m *Manager) ConnectSSH(name, user, host string, port int, password, keyPath string) (*SSHSession, error) {
	// Auto-generate name if not provided
	if name == "" {
		name = fmt.Sprintf("%s@%s", user, host)
	}

	m.mu.Lock()

	// Check if session already exists with the same name
	if existing, ok := m.sessions[name]; ok {
		if sshSess, ok := existing.(*SSHSession); ok &&
			sshSess.Host == host && sshSess.User == user && sshSess.Port == port {
			status := sshSess.GetStatus()
			if status == StatusConnected || status == StatusConnecting {
				m.defaultName = name
				m.mu.Unlock()
				return sshSess, nil
			}

			// Existing session is offline/reconnecting — try to reconnect
			sshSess.SetStatus(StatusConnecting)
			m.defaultName = name
			m.mu.Unlock()

			client, err := agssh.Connect(user, host, port, agssh.AuthConfig{Password: password, KeyPath: keyPath})
			if err != nil {
				sshSess.SetStatus(StatusOffline)
				return nil, err
			}

			m.mu.RLock()
			current, stillRegistered := m.sessions[name]
			m.mu.RUnlock()
			if !stillRegistered || current != sshSess {
				client.Close()
				return nil, fmt.Errorf("session was removed while reconnecting")
			}

			sshSess.mu.Lock()
			if sshSess.Status != StatusConnecting {
				sshSess.mu.Unlock()
				client.Close()
				return nil, fmt.Errorf("session state changed while reconnecting")
			}
			sshSess.Client = client
			sshSess.Status = StatusConnected
			sshSess.Password = password
			sshSess.KeyPath = keyPath
			sshSess.mu.Unlock()

			return sshSess, nil
		}
		// Name collision with different host — reject
		m.mu.Unlock()
		return nil, fmt.Errorf("session name '%s' already exists for a different host", name)
	}

	// Create new session
	sess := &SSHSession{
		Name:      name,
		Host:      host,
		User:      user,
		Port:      port,
		Password:  password,
		KeyPath:   keyPath,
		Status:    StatusConnecting,
		CreatedAt: time.Now(),
	}

	m.sessions[name] = sess
	m.defaultName = name
	m.mu.Unlock()

	// Connect outside of Manager lock
	client, err := agssh.Connect(user, host, port, agssh.AuthConfig{Password: password, KeyPath: keyPath})
	if err != nil {
		m.mu.Lock()
		delete(m.sessions, name)
		if m.defaultName == name {
			m.defaultName = ""
		}
		m.mu.Unlock()
		return nil, err
	}

	sess.mu.Lock()
	if sess.Status != StatusConnecting {
		// Disconnect was requested while connecting
		sess.mu.Unlock()
		client.Close()
		return nil, fmt.Errorf("session was disconnected while connecting")
	}
	sess.Client = client
	sess.Status = StatusConnected
	sess.mu.Unlock()

	return sess, nil
}

// ConnectLocal creates a local session. Only one local session exists at a time.
func (m *Manager) ConnectLocal(name string) (*LocalSession, error) {
	if name == "" {
		name = "local"
	}

	m.mu.Lock()
	// If local session already exists, return it
	if existing, ok := m.sessions[name]; ok {
		if localSess, ok := existing.(*LocalSession); ok {
			m.defaultName = name
			m.mu.Unlock()
			return localSess, nil
		}
		m.mu.Unlock()
		return nil, fmt.Errorf("session name '%s' already exists for an SSH session", name)
	}

	user, _ := osUser()
	sess := &LocalSession{
		Name:      name,
		Host:      "local",
		User:      user,
		Status:    StatusConnected,
		CreatedAt: time.Now(),
	}

	m.sessions[name] = sess
	m.defaultName = name
	m.mu.Unlock()

	return sess, nil
}

// Kill closes a session's SSH connection and removes it from the registry.
func (m *Manager) Kill(name string) error {
	if name == "" {
		m.mu.RLock()
		name = m.defaultName
		m.mu.RUnlock()
	}

	m.mu.Lock()
	sess, ok := m.sessions[name]
	if !ok {
		m.mu.Unlock()
		return fmt.Errorf("session not found")
	}

	delete(m.sessions, name)
	if m.defaultName == name {
		m.defaultName = ""
	}
	m.mu.Unlock()

	return sess.Close()
}

// List returns all sessions.
func (m *Manager) List() []Session {
	m.mu.RLock()
	defer m.mu.RUnlock()

	result := make([]Session, 0, len(m.sessions))
	for _, s := range m.sessions {
		result = append(result, s)
	}
	return result
}

// Use sets the default session and reconnects if necessary.
func (m *Manager) Use(name, password, keyPath string) error {
	if name == "" {
		name = m.GetDefaultName()
	}

	m.mu.RLock()
	sess, ok := m.sessions[name]
	m.mu.RUnlock()

	if !ok {
		return fmt.Errorf("session not found")
	}

	// Always set as default
	m.mu.Lock()
	m.defaultName = name
	m.mu.Unlock()

	if sess.IsLocal() {
		return nil
	}

	sshSess := sess.(*SSHSession)
	status := sshSess.GetStatus()

	switch status {
	case StatusConnected, StatusConnecting, StatusReconnecting:
		return nil
	case StatusOffline, StatusDisconnected:
		if password != "" {
			sshSess.SetPassword(password)
		}
		if keyPath != "" {
			sshSess.SetKeyPath(keyPath)
		}
		return m.reconnectSSH(sshSess)
	default:
		return fmt.Errorf("unexpected session status: %s", status)
	}
}

// Get returns a session by name.
func (m *Manager) Get(name string) (Session, error) {
	if name == "" {
		name = m.GetDefaultName()
	}

	m.mu.RLock()
	sess, ok := m.sessions[name]
	m.mu.RUnlock()

	if !ok {
		return nil, fmt.Errorf("session not found")
	}
	return sess, nil
}

// GetDefault returns the default session.
func (m *Manager) GetDefault() (Session, error) {
	m.mu.RLock()
	name := m.defaultName
	m.mu.RUnlock()
	return m.Get(name)
}

// GetDefaultName returns the default session name.
func (m *Manager) GetDefaultName() string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.defaultName
}

// KillAll closes all sessions (for graceful shutdown).
func (m *Manager) KillAll() {
	m.mu.Lock()
	sessions := make(map[string]Session)
	for k, v := range m.sessions {
		sessions[k] = v
	}
	m.sessions = make(map[string]Session)
	m.defaultName = ""
	m.mu.Unlock()

	for _, s := range sessions {
		s.Close()
	}
}

// RegisterOfflineSession adds an SSH session in offline state (for state restoration).
func (m *Manager) RegisterOfflineSession(name, user, host string, port int, keyPath string, createdAt time.Time) {
	sess := &SSHSession{
		Name:      name,
		Host:      host,
		User:      user,
		Port:      port,
		KeyPath:   keyPath,
		Status:    StatusOffline,
		CreatedAt: createdAt,
	}

	m.mu.Lock()
	m.sessions[name] = sess
	m.mu.Unlock()
}

// --- Internal helpers ---

func (m *Manager) reconnectSSH(sess *SSHSession) error {
	sess.SetStatus(StatusReconnecting)

	// Close existing connection if any
	sess.mu.Lock()
	oldClient := sess.Client
	sess.Client = nil
	sess.mu.Unlock()
	if oldClient != nil {
		oldClient.Close()
	}

	client, err := agssh.Connect(sess.User, sess.Host, sess.Port, agssh.AuthConfig{
		Password: sess.GetPassword(),
		KeyPath:  sess.GetKeyPath(),
	})
	if err != nil {
		sess.SetStatus(StatusOffline)
		return err
	}

	sess.mu.Lock()
	sess.Client = client
	sess.Status = StatusConnected
	sess.mu.Unlock()

	m.mu.Lock()
	m.defaultName = sess.Name
	m.mu.Unlock()

	return nil
}

func osUser() (string, error) {
	// Get current OS username
	u, err := user.Current()
	if err != nil {
		return "unknown", err
	}
	return u.Username, nil
}

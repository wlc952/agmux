package portforward

import (
	"fmt"
	"io"
	"log"
	"net"
	"sync"
	"time"

	"github.com/google/uuid"

	"agmux/internal/session"

	"golang.org/x/crypto/ssh"
)

// Forwarder handles SSH port forwarding for a single forward entry.
type Forwarder struct {
	ID         string
	Session    string // session name
	Type       string // "local" or "remote"
	LocalPort  int
	RemotePort int

	sshClient   *ssh.Client
	listener    net.Listener
	conns       map[net.Conn]bool
	closed      bool
	wg          sync.WaitGroup
	lifecycleMu sync.Mutex
	mu          sync.RWMutex
}

type remoteListenerFactory func(client *ssh.Client, addr string) (net.Listener, error)

// NewForwarder creates a new port forwarder.
func NewForwarder(sshClient *ssh.Client, forwardType string, localPort, remotePort int) (*Forwarder, error) {
	f := &Forwarder{
		ID:         uuid.New().String(),
		Type:       forwardType,
		LocalPort:  localPort,
		RemotePort: remotePort,
		sshClient:  sshClient,
		conns:      make(map[net.Conn]bool),
	}

	switch forwardType {
	case "local":
		addr := fmt.Sprintf("localhost:%d", localPort)
		listener, err := net.Listen("tcp", addr)
		if err != nil {
			return nil, fmt.Errorf("failed to listen on %s: %w", addr, err)
		}
		f.listener = listener
		log.Printf("[portforward] Local forward: localhost:%d -> remote:%d", localPort, remotePort)
	case "remote":
		log.Printf("[portforward] Remote forward: remote:%d -> localhost:%d", remotePort, localPort)
	default:
		return nil, fmt.Errorf("invalid forward type: %s", forwardType)
	}

	return f, nil
}

// Start begins accepting connections for the forwarder.
func (f *Forwarder) Start() error {
	f.lifecycleMu.Lock()
	defer f.lifecycleMu.Unlock()

	f.mu.RLock()
	if f.closed {
		f.mu.RUnlock()
		return fmt.Errorf("forwarder is closed")
	}
	f.mu.RUnlock()

	if f.Type == "local" {
		f.startLocalForward()
		return nil
	}

	return f.startRemoteForward(defaultRemoteListenerFactory)
}

func (f *Forwarder) startLocalForward() {
	f.wg.Add(1)
	go func() {
		defer f.wg.Done()
		for {
			f.mu.RLock()
			if f.closed {
				f.mu.RUnlock()
				return
			}
			listener := f.listener
			f.mu.RUnlock()

			if listener == nil {
				time.Sleep(1 * time.Second)
				continue
			}

			if tcpListener, ok := listener.(*net.TCPListener); ok {
				tcpListener.SetDeadline(time.Now().Add(5 * time.Second))
			}

			conn, err := listener.Accept()
			if err != nil {
				if netErr, ok := err.(net.Error); ok && netErr.Timeout() {
					continue
				}
				f.mu.RLock()
				closed := f.closed
				f.mu.RUnlock()
				if closed {
					return
				}
				log.Printf("[portforward] Accept error: %v", err)
				continue
			}

			f.mu.Lock()
			f.conns[conn] = true
			f.mu.Unlock()

			go f.handleLocalConnection(conn)
		}
	}()
}

func (f *Forwarder) handleLocalConnection(localConn net.Conn) {
	defer func() {
		localConn.Close()
		f.mu.Lock()
		delete(f.conns, localConn)
		f.mu.Unlock()
	}()

	remoteAddr := fmt.Sprintf("127.0.0.1:%d", f.RemotePort)

	f.mu.RLock()
	client := f.sshClient
	f.mu.RUnlock()

	if client == nil {
		log.Printf("[portforward] SSH client is nil")
		return
	}

	remoteConn, err := client.Dial("tcp", remoteAddr)
	if err != nil {
		log.Printf("[portforward] Failed to connect to remote %s: %v", remoteAddr, err)
		return
	}
	defer remoteConn.Close()

	done := make(chan struct{})
	go func() {
		io.Copy(remoteConn, localConn)
		remoteConn.Close()
		localConn.Close()
		close(done)
	}()

	io.Copy(localConn, remoteConn)
	<-done
}

func (f *Forwarder) startRemoteForward(factory remoteListenerFactory) error {
	f.mu.RLock()
	client := f.sshClient
	f.mu.RUnlock()

	if client == nil {
		return fmt.Errorf("SSH client is nil, cannot start remote forward")
	}

	remoteAddr := fmt.Sprintf("0.0.0.0:%d", f.RemotePort)
	listener, err := factory(client, remoteAddr)
	if err != nil {
		return fmt.Errorf("failed to listen on remote port %d: %w", f.RemotePort, err)
	}

	f.mu.Lock()
	f.listener = listener
	f.mu.Unlock()

	log.Printf("[portforward] Remote forward: listening on port %d", f.RemotePort)

	f.wg.Add(1)
	go func() {
		defer f.wg.Done()

		for {
			f.mu.RLock()
			if f.closed {
				f.mu.RUnlock()
				return
			}
			currentListener := f.listener
			f.mu.RUnlock()

			if currentListener == nil {
				time.Sleep(1 * time.Second)
				continue
			}

			remoteConn, err := currentListener.Accept()
			if err != nil {
				f.mu.RLock()
				closed := f.closed
				f.mu.RUnlock()
				if closed {
					return
				}
				log.Printf("[portforward] Accept error: %v", err)
				return
			}

			localAddr := fmt.Sprintf("127.0.0.1:%d", f.LocalPort)
			localConn, err := net.DialTimeout("tcp", localAddr, 5*time.Second)
			if err != nil {
				log.Printf("[portforward] Failed to connect to local %s: %v", localAddr, err)
				remoteConn.Close()
				continue
			}

			f.mu.Lock()
			f.conns[localConn] = true
			f.mu.Unlock()

			go func() {
				defer func() {
					remoteConn.Close()
					localConn.Close()
					f.mu.Lock()
					delete(f.conns, localConn)
					f.mu.Unlock()
				}()
				done := make(chan struct{})
				go func() {
					io.Copy(remoteConn, localConn)
					close(done)
				}()
				io.Copy(localConn, remoteConn)
				<-done
			}()
		}
	}()

	return nil
}

// Close shuts down the forwarder.
func (f *Forwarder) Close() {
	f.lifecycleMu.Lock()
	defer f.lifecycleMu.Unlock()

	f.mu.Lock()
	if f.closed {
		f.mu.Unlock()
		return
	}
	f.closed = true

	if f.listener != nil {
		f.listener.Close()
	}
	for conn := range f.conns {
		conn.Close()
	}
	f.mu.Unlock()

	f.wg.Wait()
}

// Restart restarts the forwarder with a new SSH client after reconnect.
func (f *Forwarder) Restart(sshClient *ssh.Client) {
	f.lifecycleMu.Lock()
	defer f.lifecycleMu.Unlock()

	// Stop existing goroutines
	f.mu.Lock()
	if !f.closed {
		f.closed = true
		if f.listener != nil {
			f.listener.Close()
		}
		for conn := range f.conns {
			conn.Close()
		}
	}
	f.conns = make(map[net.Conn]bool)
	f.mu.Unlock()

	f.wg.Wait()

	// Reset state
	f.mu.Lock()
	f.sshClient = sshClient
	f.closed = false
	f.listener = nil

	if f.Type == "local" {
		addr := fmt.Sprintf("localhost:%d", f.LocalPort)
		listener, err := net.Listen("tcp", addr)
		if err == nil {
			f.listener = listener
		} else {
			log.Printf("[portforward] Failed to recreate listener: %v", err)
		}
	}
	f.mu.Unlock()

	if f.Type == "local" {
		f.startLocalForward()
	} else if f.Type == "remote" {
		if err := f.startRemoteForward(defaultRemoteListenerFactory); err != nil {
			log.Printf("[portforward] Failed to restart remote forward: %v", err)
		}
	}
}

// --- Service layer ---

// Service manages all port forwarders.
type Service struct {
	sessions *session.Manager
	forwards map[string]*Forwarder // keyed by Forwarder.ID
	mu       sync.RWMutex
}

// NewService creates a new port forward service.
func NewService(sessions *session.Manager) *Service {
	return &Service{
		sessions: sessions,
		forwards: make(map[string]*Forwarder),
	}
}

// Add creates a new port forward for the given session.
func (s *Service) Add(sessionName, forwardType string, localPort, remotePort int) (*ForwardInfo, error) {
	sess, err := s.sessions.Get(sessionName)
	if err != nil {
		return nil, err
	}

	if sess.IsLocal() {
		return nil, fmt.Errorf("port forwarding not available for local sessions")
	}

	sshSess := sess.(*session.SSHSession)
	sshClient := sshSess.GetSSHClient()
	if sshClient == nil {
		return nil, fmt.Errorf("session not connected")
	}

	forwarder, err := NewForwarder(sshClient, forwardType, localPort, remotePort)
	if err != nil {
		return nil, err
	}

	forwarder.Session = sess.GetName()

	s.mu.Lock()
	s.forwards[forwarder.ID] = forwarder
	s.mu.Unlock()

	if err := forwarder.Start(); err != nil {
		s.mu.Lock()
		delete(s.forwards, forwarder.ID)
		s.mu.Unlock()
		return nil, err
	}

	return &ForwardInfo{
		ID:         forwarder.ID,
		Session:    forwarder.Session,
		Type:       forwarder.Type,
		LocalPort:  forwarder.LocalPort,
		RemotePort: forwarder.RemotePort,
	}, nil
}

func defaultRemoteListenerFactory(client *ssh.Client, addr string) (net.Listener, error) {
	return client.Listen("tcp", addr)
}

// List returns all active port forwards.
func (s *Service) List() []*ForwardInfo {
	s.mu.RLock()
	defer s.mu.RUnlock()

	result := make([]*ForwardInfo, 0, len(s.forwards))
	for _, f := range s.forwards {
		result = append(result, &ForwardInfo{
			ID:         f.ID,
			Session:    f.Session,
			Type:       f.Type,
			LocalPort:  f.LocalPort,
			RemotePort: f.RemotePort,
		})
	}
	return result
}

// Close shuts down a specific port forward.
func (s *Service) Close(forwardID string) error {
	s.mu.Lock()
	forwarder, ok := s.forwards[forwardID]
	if !ok {
		s.mu.Unlock()
		return fmt.Errorf("forward not found")
	}
	delete(s.forwards, forwardID)
	s.mu.Unlock()

	forwarder.Close()
	return nil
}

// RestartAll restarts all forwards belonging to a session after reconnect.
func (s *Service) RestartAll(sessionName string, sshClient *ssh.Client) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	for _, f := range s.forwards {
		if f.Session == sessionName {
			f.Restart(sshClient)
		}
	}
}

// ForwardInfo is the serialized representation of a port forward.
type ForwardInfo struct {
	ID         string `json:"id"`
	Session    string `json:"session"`
	Type       string `json:"type"`
	LocalPort  int    `json:"local_port"`
	RemotePort int    `json:"remote_port"`
}

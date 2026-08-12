package reconnect

import (
	"log"
	"sync"
	"time"

	"gssh/internal/portforward"
	"gssh/internal/session"
)

// Monitor watches SSH sessions for disconnection and reconnects with exponential backoff.
type Monitor struct {
	sessions *session.Manager
	forwards *portforward.Service
	watched  map[string]watchCtx
	mu       sync.Mutex
}

type watchCtx struct {
	cancel  chan struct{}
	backoff time.Duration
}

const (
	monitorInterval = 5 * time.Second
	initialBackoff  = 5 * time.Second
	maxBackoff      = 5 * time.Minute
	backoffFactor   = 2
)

// NewMonitor creates a new reconnect monitor.
func NewMonitor(sessions *session.Manager, forwards *portforward.Service) *Monitor {
	return &Monitor{
		sessions: sessions,
		forwards: forwards,
		watched:  make(map[string]watchCtx),
	}
}

// Watch starts monitoring an SSH session for disconnection.
func (m *Monitor) Watch(sess *session.SSHSession) {
	m.mu.Lock()
	if _, ok := m.watched[sess.GetName()]; ok {
		m.mu.Unlock()
		return
	}
	ctx := watchCtx{
		cancel:  make(chan struct{}),
		backoff: initialBackoff,
	}
	m.watched[sess.GetName()] = ctx
	m.mu.Unlock()

	go m.monitorLoop(sess, ctx)
}

// StopWatch stops monitoring a session.
func (m *Monitor) StopWatch(name string) {
	m.mu.Lock()
	ctx, ok := m.watched[name]
	if ok {
		close(ctx.cancel)
		delete(m.watched, name)
	}
	m.mu.Unlock()
}

func (m *Monitor) monitorLoop(sess *session.SSHSession, ctx watchCtx) {
	delay := monitorInterval

	for {
		timer := time.NewTimer(delay)
		select {
		case <-ctx.cancel:
			timer.Stop()
			return
		case <-timer.C:
			delay = m.checkAndReconnect(sess, &ctx)
		}
	}
}

func (m *Monitor) checkAndReconnect(sess *session.SSHSession, ctx *watchCtx) time.Duration {
	status := sess.GetStatus()
	if status == session.StatusDisconnected {
		m.StopWatch(sess.GetName())
		return monitorInterval
	}
	if status == session.StatusConnecting || status == session.StatusReconnecting {
		return monitorInterval
	}

	client := sess.GetClient()
	if client != nil && client.IsAlive() {
		ctx.backoff = initialBackoff
		return monitorInterval
	}

	name := sess.GetName()
	log.Printf("[reconnect] Session %s appears dead, reconnecting", name)

	// Delegate to the manager's serialized reconnect path: it closes the old
	// client, dials with stored credentials, and re-validates registration
	// before adopting the new one.
	if _, err := m.sessions.Reconnect(name); err != nil {
		log.Printf("[reconnect] Failed to reconnect %s: %v (backoff: %v)", name, err, ctx.backoff)
		delay := ctx.backoff
		ctx.backoff = min(ctx.backoff*time.Duration(backoffFactor), maxBackoff)
		return delay
	}

	ctx.backoff = initialBackoff

	if m.forwards != nil {
		m.forwards.RestartAll(name, sess.GetSSHClient())
	}

	log.Printf("[reconnect] Session %s reconnected successfully", name)
	return monitorInterval
}

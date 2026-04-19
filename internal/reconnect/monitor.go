package reconnect

import (
	"log"
	"sync"
	"time"

	"agmux/internal/portforward"
	"agmux/internal/session"
	agssh "agmux/internal/ssh"
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
	initialBackoff = 5 * time.Second
	maxBackoff     = 5 * time.Minute
	backoffFactor  = 2
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
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.cancel:
			return
		case <-ticker.C:
			m.checkAndReconnect(sess, &ctx)
		}
	}
}

func (m *Monitor) checkAndReconnect(sess *session.SSHSession, ctx *watchCtx) {
	status := sess.GetStatus()
	if status == session.StatusDisconnected {
		m.StopWatch(sess.GetName())
		return
	}
	if status == session.StatusConnecting || status == session.StatusReconnecting {
		return
	}

	client := sess.GetClient()
	if client == nil || !client.IsAlive() {
		log.Printf("[reconnect] Session %s appears dead, reconnecting", sess.GetName())

		sess.SetStatus(session.StatusReconnecting)
		if client != nil {
			client.Close()
			sess.SetClient(nil)
		}

		newClient, err := agssh.Connect(sess.User, sess.Host, sess.Port, agssh.AuthConfig{
			Password: sess.GetPassword(),
			KeyPath:  sess.GetKeyPath(),
		})
		if err != nil {
			log.Printf("[reconnect] Failed to reconnect %s: %v (backoff: %v)", sess.GetName(), err, ctx.backoff)
			sess.SetStatus(session.StatusOffline)
			time.Sleep(ctx.backoff)
			ctx.backoff = min(ctx.backoff*time.Duration(backoffFactor), maxBackoff)
			return
		}

		sess.SetClient(newClient)
		sess.SetStatus(session.StatusConnected)

		ctx.backoff = initialBackoff

		if m.forwards != nil {
			m.forwards.RestartAll(sess.GetName(), newClient.GoClient)
		}

		log.Printf("[reconnect] Session %s reconnected successfully", sess.GetName())
	}
}
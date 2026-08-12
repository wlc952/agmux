package reconnect

import (
	"fmt"
	"testing"
	"time"

	"gssh/internal/session"
	sshclient "gssh/internal/ssh"
)

func TestCheckAndReconnectFailureReturnsBackoff(t *testing.T) {
	mgr := session.NewManager()
	mgr.SetConnectFunc(func(user, host string, port int, auth sshclient.AuthConfig) (*sshclient.Client, error) {
		return nil, fmt.Errorf("dial failed")
	})
	mgr.RegisterOfflineSession("prod", "root", "example.com", 22, "", time.Now())
	mon := NewMonitor(mgr, nil)

	sessIface, err := mgr.Get("prod")
	if err != nil {
		t.Fatalf("Get failed: %v", err)
	}
	sess := sessIface.(*session.SSHSession)
	ctx := &watchCtx{cancel: make(chan struct{}), backoff: initialBackoff}

	delay := mon.checkAndReconnect(sess, ctx)

	if delay != initialBackoff {
		t.Fatalf("delay = %v, want %v", delay, initialBackoff)
	}
	if ctx.backoff != initialBackoff*time.Duration(backoffFactor) {
		t.Fatalf("next backoff = %v, want %v", ctx.backoff, initialBackoff*time.Duration(backoffFactor))
	}
	if sess.GetStatus() != session.StatusOffline {
		t.Fatalf("status = %s, want offline", sess.GetStatus())
	}
}

func TestCheckAndReconnectDisconnectedStopsWatching(t *testing.T) {
	mon := NewMonitor(session.NewManager(), nil)
	sess := &session.SSHSession{
		Name:      "prod",
		Status:    session.StatusDisconnected,
		CreatedAt: time.Now(),
	}
	ctx := watchCtx{cancel: make(chan struct{}), backoff: initialBackoff}
	mon.watched[sess.Name] = ctx

	delay := mon.checkAndReconnect(sess, &ctx)

	if delay != monitorInterval {
		t.Fatalf("delay = %v, want %v", delay, monitorInterval)
	}
	if _, ok := mon.watched[sess.Name]; ok {
		t.Fatal("expected watch to be removed")
	}
}

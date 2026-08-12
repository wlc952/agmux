package session

import (
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
	sshclient "gssh/internal/ssh"
)

func TestNewManager(t *testing.T) {
	m := NewManager()
	if m == nil {
		t.Fatal("NewManager returned nil")
	}
	if len(m.sessions) != 0 {
		t.Errorf("expected empty sessions map")
	}
	if m.GetDefaultName() != "" {
		t.Errorf("expected empty default name")
	}
}

func TestConnectLocal(t *testing.T) {
	m := NewManager()

	sess, err := m.ConnectLocal("local")
	if err != nil {
		t.Fatalf("ConnectLocal failed: %v", err)
	}

	if sess.GetName() != "local" {
		t.Errorf("Name = %s, want local", sess.GetName())
	}
	if sess.GetType() != "local" {
		t.Errorf("Type = %s, want local", sess.GetType())
	}
	if !sess.IsLocal() {
		t.Error("IsLocal should be true")
	}
	if sess.GetStatus() != StatusConnected {
		t.Errorf("Status = %s, want connected", sess.GetStatus())
	}
	if m.GetDefaultName() != "local" {
		t.Errorf("Default = %s, want local", m.GetDefaultName())
	}

	// Duplicate local session returns same
	sess2, err := m.ConnectLocal("local")
	if err != nil {
		t.Fatalf("ConnectLocal duplicate failed: %v", err)
	}
	if sess2.GetName() != sess.GetName() {
		t.Error("duplicate should return same session")
	}
}

func TestUseLocal(t *testing.T) {
	m := NewManager()
	_, _ = m.ConnectLocal("local")

	err := m.Use("local", "", "")
	if err != nil {
		t.Fatalf("Use failed: %v", err)
	}
	if m.GetDefaultName() != "local" {
		t.Errorf("Default = %s, want local", m.GetDefaultName())
	}

	err = m.Use("nonexistent", "", "")
	if err == nil {
		t.Error("Use should fail for nonexistent session")
	}
}

func TestUseSetsDefaultOnNewSession(t *testing.T) {
	m := NewManager()

	// First local session becomes default
	_, _ = m.ConnectLocal("first")
	if m.GetDefaultName() != "first" {
		t.Errorf("Default = %s, want first", m.GetDefaultName())
	}

	// Second session also becomes default (not only when empty)
	_, _ = m.ConnectLocal("second")
	if m.GetDefaultName() != "second" {
		t.Errorf("Default = %s, want second (new session should always become default)", m.GetDefaultName())
	}
}

func TestKill(t *testing.T) {
	m := NewManager()
	_, _ = m.ConnectLocal("local")

	err := m.Kill("local")
	if err != nil {
		t.Fatalf("Kill failed: %v", err)
	}

	sessions := m.List()
	if len(sessions) != 0 {
		t.Errorf("expected empty sessions after kill, got %d", len(sessions))
	}
	if m.GetDefaultName() != "" {
		t.Errorf("default should be empty after kill")
	}
}

func TestStatusConstants(t *testing.T) {
	if StatusConnected != "connected" {
		t.Errorf("StatusConnected = %s", StatusConnected)
	}
	if StatusDisconnected != "disconnected" {
		t.Errorf("StatusDisconnected = %s", StatusDisconnected)
	}
	if StatusReconnecting != "reconnecting" {
		t.Errorf("StatusReconnecting = %s", StatusReconnecting)
	}
	if StatusOffline != "offline" {
		t.Errorf("StatusOffline = %s", StatusOffline)
	}
	if StatusConnecting != "connecting" {
		t.Errorf("StatusConnecting = %s", StatusConnecting)
	}
}

func TestAwaitClientTerminalStatesFailFast(t *testing.T) {
	sess := &SSHSession{Name: "s", Status: StatusOffline, CreatedAt: time.Now()}
	if _, err := sess.AwaitClient(5 * time.Second); err == nil {
		t.Fatal("expected immediate error for offline session")
	}

	sess.SetStatus(StatusDisconnected)
	if _, err := sess.AwaitClient(5 * time.Second); err == nil {
		t.Fatal("expected immediate error for disconnected session")
	}
}

func TestAwaitClientTimesOutWhileConnecting(t *testing.T) {
	sess := &SSHSession{Name: "s", Status: StatusConnecting, CreatedAt: time.Now()}
	start := time.Now()
	if _, err := sess.AwaitClient(150 * time.Millisecond); err == nil {
		t.Fatal("expected timeout error")
	}
	if elapsed := time.Since(start); elapsed < 150*time.Millisecond {
		t.Fatalf("returned too early: %v", elapsed)
	}
}

func TestAwaitClientReturnsWhenConnected(t *testing.T) {
	sess := &SSHSession{Name: "s", Status: StatusConnecting, CreatedAt: time.Now()}
	go func() {
		time.Sleep(50 * time.Millisecond)
		sess.mu.Lock()
		sess.Client = &sshclient.Client{GoClient: &ssh.Client{}}
		sess.Status = StatusConnected
		sess.mu.Unlock()
	}()

	if _, err := sess.AwaitClient(5 * time.Second); err != nil {
		t.Fatalf("expected client, got %v", err)
	}
}

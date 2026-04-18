package session

import (
	"testing"
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

func TestUse(t *testing.T) {
	m := NewManager()
	_, _ = m.ConnectLocal("local")

	err := m.Use("local")
	if err != nil {
		t.Fatalf("Use failed: %v", err)
	}
	if m.GetDefaultName() != "local" {
		t.Errorf("Default = %s, want local", m.GetDefaultName())
	}

	err = m.Use("nonexistent")
	if err == nil {
		t.Error("Use should fail for nonexistent session")
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

func TestDetachLocalFails(t *testing.T) {
	m := NewManager()
	_, _ = m.ConnectLocal("local")

	err := m.Detach("local")
	if err == nil {
		t.Error("Detach should fail for local session")
	}
}

func TestStatusConstants(t *testing.T) {
	if StatusConnected != "connected" {
		t.Errorf("StatusConnected = %s", StatusConnected)
	}
	if StatusDetached != "detached" {
		t.Errorf("StatusDetached = %s", StatusDetached)
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
}
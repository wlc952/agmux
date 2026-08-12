package persist

import (
	"testing"
	"time"

	"gssh/internal/session"
)

func TestSessionToStateSSHIncludesReconnectMetadata(t *testing.T) {
	createdAt := time.Unix(1710000000, 0)
	sess := &session.SSHSession{
		Name:      "prod",
		Host:      "example.com",
		User:      "root",
		Port:      2222,
		KeyPath:   "~/.ssh/id_ed25519",
		Status:    session.StatusOffline,
		CreatedAt: createdAt,
	}

	state := SessionToState(sess)

	if state.Port != 2222 {
		t.Fatalf("Port = %d, want 2222", state.Port)
	}
	if state.KeyPath != "~/.ssh/id_ed25519" {
		t.Fatalf("KeyPath = %q, want ~/.ssh/id_ed25519", state.KeyPath)
	}
	if state.CreatedAt != createdAt.Unix() {
		t.Fatalf("CreatedAt = %d, want %d", state.CreatedAt, createdAt.Unix())
	}
}

func TestSessionToStateLocalOmitsSSHMetadata(t *testing.T) {
	createdAt := time.Unix(1710000001, 0)
	sess := &session.LocalSession{
		Name:      "local",
		Host:      "local",
		User:      "tester",
		Status:    session.StatusConnected,
		CreatedAt: createdAt,
	}

	state := SessionToState(sess)

	if state.Port != 0 {
		t.Fatalf("Port = %d, want 0", state.Port)
	}
	if state.KeyPath != "" {
		t.Fatalf("KeyPath = %q, want empty", state.KeyPath)
	}
}

func TestSessionToStateAutoReconnect(t *testing.T) {
	tests := []struct {
		name     string
		password string
		keyPath  string
		want     bool
	}{
		{name: "password only cannot auto-reconnect", password: "secret", want: false},
		{name: "explicit key can auto-reconnect", keyPath: "~/.ssh/id_ed25519", want: true},
		{name: "key and password can auto-reconnect via key", password: "secret", keyPath: "~/.ssh/id_ed25519", want: true},
		{name: "default key material can auto-reconnect", want: true},
	}

	for _, tt := range tests {
		sess := &session.SSHSession{
			Name:     "s",
			Host:     "h",
			User:     "u",
			Status:   session.StatusConnected,
			Password: tt.password,
			KeyPath:  tt.keyPath,
		}
		if got := SessionToState(sess).AutoReconnect; got != tt.want {
			t.Errorf("%s: AutoReconnect = %v, want %v", tt.name, got, tt.want)
		}
	}
}

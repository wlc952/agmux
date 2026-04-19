package portforward

import (
	"fmt"
	"net"
	"testing"

	"golang.org/x/crypto/ssh"
)

func TestNewForwarderRejectsInvalidType(t *testing.T) {
	if _, err := NewForwarder(nil, "invalid", 1000, 2000, ""); err == nil {
		t.Fatal("expected invalid forward type error")
	}
}

func TestNewForwarderDefaultsRemoteBindToLoopback(t *testing.T) {
	forwarder, err := NewForwarder(nil, "remote", 3000, 9000, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if forwarder.BindAddr != "127.0.0.1" {
		t.Fatalf("BindAddr = %q, want 127.0.0.1", forwarder.BindAddr)
	}
}

func TestStartRemoteForwardReturnsErrorWhenClientMissing(t *testing.T) {
	forwarder := &Forwarder{
		Type:       "remote",
		LocalPort:  3000,
		RemotePort: 9000,
		conns:      make(map[net.Conn]bool),
	}

	if err := forwarder.startRemoteForward(defaultRemoteListenerFactory); err == nil {
		t.Fatal("expected nil ssh client error")
	}
}

func TestStartRemoteForwardPropagatesFactoryError(t *testing.T) {
	forwarder := &Forwarder{
		Type:       "remote",
		LocalPort:  3000,
		RemotePort: 9000,
		conns:      make(map[net.Conn]bool),
		sshClient:  &ssh.Client{},
	}

	err := forwarder.startRemoteForward(func(_ *ssh.Client, addr string) (net.Listener, error) {
		return nil, fmt.Errorf("listen failed on %s", addr)
	})
	if err == nil {
		t.Fatal("expected startup error")
	}
}

func TestStartRemoteForwardUsesJoinHostPortForIPv6(t *testing.T) {
	forwarder := &Forwarder{
		Type:       "remote",
		LocalPort:  3000,
		RemotePort: 9000,
		BindAddr:   "::1",
		conns:      make(map[net.Conn]bool),
		sshClient:  &ssh.Client{},
	}

	var gotAddr string
	_ = forwarder.startRemoteForward(func(_ *ssh.Client, addr string) (net.Listener, error) {
		gotAddr = addr
		return nil, fmt.Errorf("stop")
	})

	if gotAddr != "[::1]:9000" {
		t.Fatalf("remote address = %q, want [::1]:9000", gotAddr)
	}
}

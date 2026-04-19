package portforward

import (
	"fmt"
	"net"
	"testing"

	"golang.org/x/crypto/ssh"
)

func TestNewForwarderRejectsInvalidType(t *testing.T) {
	if _, err := NewForwarder(nil, "invalid", 1000, 2000); err == nil {
		t.Fatal("expected invalid forward type error")
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

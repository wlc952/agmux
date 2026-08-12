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

func newStubForwarder(id string) *Forwarder {
	return &Forwarder{ID: id, Type: "local", conns: make(map[net.Conn]bool)}
}

func TestServiceCloseByFullID(t *testing.T) {
	svc := &Service{forwards: map[string]*Forwarder{"abcd1234-full-uuid": newStubForwarder("abcd1234-full-uuid")}}

	if err := svc.Close("abcd1234-full-uuid"); err != nil {
		t.Fatalf("Close(full ID) failed: %v", err)
	}
	if len(svc.forwards) != 0 {
		t.Fatal("forwarder was not removed")
	}
}

func TestServiceCloseByUniquePrefix(t *testing.T) {
	svc := &Service{forwards: map[string]*Forwarder{
		"aaaa1111-full-uuid": newStubForwarder("aaaa1111-full-uuid"),
		"bbbb2222-full-uuid": newStubForwarder("bbbb2222-full-uuid"),
	}}

	if err := svc.Close("aaaa1111"); err != nil {
		t.Fatalf("Close(prefix) failed: %v", err)
	}
	if _, ok := svc.forwards["bbbb2222-full-uuid"]; !ok {
		t.Fatal("unrelated forwarder was removed")
	}
}

func TestServiceCloseAmbiguousPrefix(t *testing.T) {
	svc := &Service{forwards: map[string]*Forwarder{
		"aaaa1111-full-uuid": newStubForwarder("aaaa1111-full-uuid"),
		"aaaa2222-full-uuid": newStubForwarder("aaaa2222-full-uuid"),
	}}

	if err := svc.Close("aaaa"); err == nil {
		t.Fatal("expected ambiguous prefix error")
	}
	if len(svc.forwards) != 2 {
		t.Fatal("forwarders were removed despite ambiguity")
	}
}

func TestServiceCloseRejectsEmptyAndUnknownID(t *testing.T) {
	svc := &Service{forwards: map[string]*Forwarder{"abcd1234-full-uuid": newStubForwarder("abcd1234-full-uuid")}}

	if err := svc.Close(""); err == nil {
		t.Fatal("expected error for empty ID")
	}
	if err := svc.Close("ffff"); err == nil {
		t.Fatal("expected error for unknown ID")
	}
}

//go:build linux

package main

import (
	"fmt"
	"net"
	"os"

	"golang.org/x/sys/unix"
)

func verifyDaemonPeer(conn net.Conn) error {
	unixConn, ok := conn.(*net.UnixConn)
	if !ok {
		return fmt.Errorf("refusing daemon connection: not a unix socket")
	}

	rawConn, err := unixConn.SyscallConn()
	if err != nil {
		return fmt.Errorf("failed to inspect daemon peer credentials: %w", err)
	}

	var cred *unix.Ucred
	var credErr error
	if err := rawConn.Control(func(fd uintptr) {
		cred, credErr = unix.GetsockoptUcred(int(fd), unix.SOL_SOCKET, unix.SO_PEERCRED)
	}); err != nil {
		return fmt.Errorf("failed to inspect daemon peer credentials: %w", err)
	}
	if credErr != nil {
		return fmt.Errorf("failed to inspect daemon peer credentials: %w", credErr)
	}
	if cred == nil {
		return fmt.Errorf("failed to inspect daemon peer credentials: empty credentials")
	}
	if int(cred.Uid) != os.Geteuid() {
		return fmt.Errorf("refusing daemon connection: peer uid %d, expected %d", cred.Uid, os.Geteuid())
	}
	return nil
}

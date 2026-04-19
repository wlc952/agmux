//go:build !darwin && !linux

package main

import "net"

func verifyDaemonPeer(_ net.Conn) error {
	return nil
}

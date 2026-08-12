//go:build unix

package main

import (
	"os/exec"
	"syscall"
)

// setDetachAttrs puts the daemon in its own session so it survives the
// calling terminal closing (no SIGHUP) and is fully detached.
func setDetachAttrs(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
}

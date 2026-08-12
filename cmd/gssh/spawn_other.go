//go:build !unix

package main

import "os/exec"

// setDetachAttrs is a no-op on platforms without POSIX sessions.
func setDetachAttrs(cmd *exec.Cmd) {}

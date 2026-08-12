package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/user"
	"strconv"
	"strings"

	"gssh/internal/protocol"
)

// parseDestination splits an ssh-style destination "user@host" or "host".
// A missing user defaults to the current OS user (like ssh).
func parseDestination(dest string) (string, string, error) {
	u, h := "", dest
	if i := strings.LastIndex(dest, "@"); i >= 0 {
		u, h = dest[:i], dest[i+1:]
	}
	if u == "" {
		current, err := user.Current()
		if err != nil {
			return "", "", fmt.Errorf("no user in destination %q and cannot determine current user: %w", dest, err)
		}
		u = current.Username
	}
	if h == "" {
		return "", "", fmt.Errorf("empty host in destination %q", dest)
	}
	return u, h, nil
}

// handleDestination implements the ssh-style shorthand:
//
//	gssh user@host [flags]                → connect (create/reuse session)
//	gssh user@host [flags] command...     → connect if needed, then exec
//
// Sessions are reused via the server's connect dedup, so repeated one-off
// execs against the same host share one SSH connection.
func handleDestination(dest string, args []string) error {
	fs := flag.NewFlagSet("ssh", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)

	port := fs.Int("p", 22, "Port")
	keyPath := fs.String("i", "", "SSH key path")
	password := fs.String("pswd", "", "Password (SSH auth; also used for sudo when --sudo is set)")
	name := fs.String("n", "", "Session name")
	timeout := fs.Int("t", 0, "Timeout in seconds")
	stream := fs.Bool("stream", false, "Stream output in real-time")
	rawOut := fs.Bool("raw", false, "Print raw stdout/stderr instead of JSON")
	_ = fs.Bool("json", false, "(deprecated) JSON is the default; kept for compatibility")
	useSudo := fs.Bool("sudo", false, "Run with sudo")
	sudoUser := fs.String("sudo-user", "", "Run as specified user")
	sudoLogin := fs.Bool("sudo-login", false, "Login shell (-i)")

	if err := fs.Parse(args); err != nil {
		return err
	}

	userName, host, err := parseDestination(dest)
	if err != nil {
		return err
	}

	params := protocol.ConnectParams{
		Name:     *name,
		User:     userName,
		Host:     host,
		Port:     *port,
		Password: *password,
		KeyPath:  *keyPath,
	}

	result, err := sendRequestWithTimeout(protocol.MsgConnect, params, connectRPCTimeout)
	if err != nil {
		return err
	}

	var info protocol.SessionInfo
	if err := json.Unmarshal(result, &info); err != nil {
		return fmt.Errorf("failed to parse result: %w", err)
	}

	if fs.NArg() == 0 {
		// Connect only.
		fmt.Printf("Connected: %s@%s (name: %s, type: %s)\n", info.User, info.Host, info.Name, info.Type)
		return nil
	}

	// Exec on the (new or existing) session. --pswd doubles as the sudo
	// password: it is the same account password in the common case.
	execParams := protocol.ExecParams{
		Name:    info.Name,
		Command: joinCommandArgs(fs.Args()),
		Timeout: *timeout,
		Sudo: protocol.SudoOptions{
			Enabled:  *useSudo,
			Password: *password,
			User:     *sudoUser,
			Login:    *sudoLogin,
		},
	}

	if *stream {
		return sendStreamRequest(protocol.MsgExecStream, execParams, *timeout)
	}

	execResult, err := sendRequestWithCmdTimeout(protocol.MsgExec, execParams, *timeout)
	if err != nil {
		return err
	}
	return printExecResult(execResult, *rawOut)
}

// splitRemoteSpec parses a scp-style endpoint "session:path". Session names
// never contain ':' or path separators, so any prefix containing '/' or '\'
// is treated as a local path, as is the Windows drive form "X:\". A
// single-letter prefix followed by something else (e.g. "a:/dst") is a
// session named "a" — only the backslash form counts as a drive letter.
// An empty path after the colon means the remote home directory (".").
func splitRemoteSpec(s string) (sessionName, path string, ok bool) {
	i := strings.Index(s, ":")
	if i < 1 {
		return "", "", false
	}
	prefix := s[:i]
	if strings.ContainsAny(prefix, `/\`) {
		return "", "", false
	}
	if len(prefix) == 1 && strings.HasPrefix(s[i+1:], `\`) {
		return "", "", false // Windows drive path, e.g. C:\data
	}
	path = s[i+1:]
	if path == "" {
		path = "."
	}
	return prefix, path, true
}

// parseForwardSpec parses an ssh-style forward spec "portA:portB".
// For -L the order is localPort:remotePort; for -R it is remotePort:localPort
// (matching ssh exactly).
func parseForwardSpec(spec string) (int, int, error) {
	parts := strings.Split(spec, ":")
	if len(parts) != 2 {
		return 0, 0, fmt.Errorf("invalid forward spec %q: want <port>:<port>", spec)
	}
	a, errA := strconv.Atoi(parts[0])
	b, errB := strconv.Atoi(parts[1])
	if errA != nil || errB != nil || a < 1 || a > 65535 || b < 1 || b > 65535 {
		return 0, 0, fmt.Errorf("invalid forward spec %q: both ports must be 1-65535", spec)
	}
	return a, b, nil
}

package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"net"
	"os"
	"strings"
	"time"

	"gssh/internal/protocol"
	"gssh/internal/socketpath"
)

func handleStart() error {
	// Check if daemon is already running
	if _, err := os.Lstat(socketPath); err == nil {
		if err := socketpath.Validate(socketPath); err != nil {
			return err
		}
		conn, err := net.DialTimeout("unix", socketPath, 2*time.Second)
		if err == nil {
			if err := verifyDaemonPeer(conn); err != nil {
				conn.Close()
				return err
			}
			conn.Close()
			fmt.Println("Daemon is already running")
			return nil
		}
		// Stale socket: the freshly spawned server removes it before listening.
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("failed to inspect socket path: %w", err)
	}

	if err := startDaemon(); err != nil {
		return fmt.Errorf("failed to start daemon: %w", err)
	}

	conn, err := waitForDaemon()
	if err != nil {
		return err
	}
	conn.Close()

	fmt.Printf("Daemon started (socket: %s, log: %s)\n", socketPath, daemonLogPath())
	return nil
}

func handleConnect(args []string) error {
	fs := flag.NewFlagSet("connect", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)

	name := fs.String("n", "", "Session name")
	user := fs.String("u", "", "Username")
	host := fs.String("h", "", "Host")
	port := fs.Int("P", 22, "Port")
	password := fs.String("p", "", "Password")
	keyPath := fs.String("i", "", "SSH key path")

	fs.Parse(args)

	if *user == "" || *host == "" {
		return fmt.Errorf("user and host are required")
	}

	params := protocol.ConnectParams{
		Name:     *name,
		User:     *user,
		Host:     *host,
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

	fmt.Printf("Connected: %s@%s (name: %s, type: %s)\n", info.User, info.Host, info.Name, info.Type)
	return nil
}

func handleLocal(args []string) error {
	fs := flag.NewFlagSet("local", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)

	name := fs.String("n", "", "Session name")
	fs.Parse(args)

	params := protocol.LocalParams{Name: *name}

	result, err := sendRequest(protocol.MsgLocal, params)
	if err != nil {
		return err
	}

	var info protocol.SessionInfo
	if err := json.Unmarshal(result, &info); err != nil {
		return fmt.Errorf("failed to parse result: %w", err)
	}

	fmt.Printf("Local session created: %s\n", info.Name)
	return nil
}

func handleUse(args []string) error {
	fs := flag.NewFlagSet("use", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	password := fs.String("p", "", "Password (for reconnecting offline session)")
	keyPath := fs.String("i", "", "SSH key path (for reconnecting offline session)")
	fs.Parse(args)

	if fs.NArg() < 1 {
		return fmt.Errorf("session name required")
	}

	_, err := sendRequestWithTimeout(protocol.MsgUse, protocol.UseParams{
		Name:     fs.Arg(0),
		Password: *password,
		KeyPath:  *keyPath,
	}, connectRPCTimeout)
	if err != nil {
		return err
	}

	fmt.Printf("Using session: %s\n", fs.Arg(0))
	return nil
}

func handleKill(args []string) error {
	fs := flag.NewFlagSet("kill", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	name := fs.String("n", "", "Session name")
	fs.Parse(args)

	// Support positional argument
	nameStr := *name
	if nameStr == "" && fs.NArg() > 0 {
		nameStr = fs.Arg(0)
	}

	_, err := sendRequest(protocol.MsgKill, protocol.KillParams{Name: nameStr})
	if err != nil {
		return err
	}

	fmt.Println("Killed")
	return nil
}

// printExecResult writes an exec result to stdout/stderr, either as raw
// streams (default) or as a single JSON object (--json), and exits with the
// command's exit code when non-zero.
func printExecResult(result []byte, jsonOut bool) error {
	var execResult protocol.ExecResult
	if err := json.Unmarshal(result, &execResult); err != nil {
		return fmt.Errorf("failed to parse result: %w", err)
	}

	if jsonOut {
		data, err := json.Marshal(execResult)
		if err != nil {
			return fmt.Errorf("failed to encode result: %w", err)
		}
		fmt.Println(string(data))
	} else {
		fmt.Print(execResult.Stdout)
		if execResult.Stderr != "" {
			fmt.Fprintf(os.Stderr, "%s", execResult.Stderr)
		}
	}

	if execResult.ExitCode != 0 {
		os.Exit(execResult.ExitCode)
	}
	return nil
}

func handleExec(args []string) error {
	fs := flag.NewFlagSet("exec", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)

	name := fs.String("n", "", "Session name")
	timeout := fs.Int("t", 0, "Timeout in seconds")
	useSudo := fs.Bool("sudo", false, "Run with sudo")
	sudoPassword := fs.String("sudo-password", "", "Sudo password")
	sudoUser := fs.String("sudo-user", "", "Run as specified user")
	sudoLogin := fs.Bool("sudo-login", false, "Login shell (-i)")
	stream := fs.Bool("stream", false, "Stream output in real-time")
	jsonOut := fs.Bool("json", false, "Print result as JSON")

	fs.Parse(args)

	if fs.NArg() < 1 {
		return fmt.Errorf("command required")
	}
	command := joinCommandArgs(fs.Args())

	sudoOpts := protocol.SudoOptions{
		Enabled:  *useSudo,
		Password: *sudoPassword,
		User:     *sudoUser,
		Login:    *sudoLogin,
	}

	params := protocol.ExecParams{
		Name:    *name,
		Command: command,
		Timeout: *timeout,
		Sudo:    sudoOpts,
	}

	if *stream {
		return sendStreamRequest(protocol.MsgExecStream, params, *timeout)
	}

	result, err := sendRequestWithCmdTimeout(protocol.MsgExec, params, *timeout)
	if err != nil {
		return err
	}

	return printExecResult(result, *jsonOut)
}

func handleRun(args []string) error {
	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)

	timeout := fs.Int("t", 0, "Timeout in seconds")
	useSudo := fs.Bool("sudo", false, "Run with sudo")
	sudoPassword := fs.String("sudo-password", "", "Sudo password")
	sudoUser := fs.String("sudo-user", "", "Run as specified user")
	sudoLogin := fs.Bool("sudo-login", false, "Login shell (-i)")
	stream := fs.Bool("stream", false, "Stream output in real-time")
	jsonOut := fs.Bool("json", false, "Print result as JSON")

	fs.Parse(args)

	if fs.NArg() < 1 {
		return fmt.Errorf("command required")
	}
	command := joinCommandArgs(fs.Args())

	sudoOpts := protocol.SudoOptions{
		Enabled:  *useSudo,
		Password: *sudoPassword,
		User:     *sudoUser,
		Login:    *sudoLogin,
	}

	params := protocol.LocalExecParams{
		Command: command,
		Timeout: *timeout,
		Sudo:    sudoOpts,
	}

	if *stream {
		return sendStreamRequest(protocol.MsgLocalExecStream, params, *timeout)
	}

	result, err := sendRequestWithCmdTimeout(protocol.MsgLocalExec, params, *timeout)
	if err != nil {
		return err
	}

	return printExecResult(result, *jsonOut)
}

func joinCommandArgs(args []string) string {
	switch len(args) {
	case 0:
		return ""
	case 1:
		// Preserve single-argument commands verbatim so shell syntax like pipes
		// still works when the caller intentionally passed one complete string.
		return args[0]
	default:
		quoted := make([]string, len(args))
		for i, arg := range args {
			quoted[i] = shellQuote(arg)
		}
		return strings.Join(quoted, " ")
	}
}

func shellQuote(arg string) string {
	if arg == "" {
		return "''"
	}

	return "'" + strings.ReplaceAll(arg, "'", `'"'"'`) + "'"
}

func handleList(args []string) error {
	fs := flag.NewFlagSet("list", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	jsonOut := fs.Bool("json", false, "Print result as JSON")
	fs.Parse(args)

	result, err := sendRequest(protocol.MsgList, nil)
	if err != nil {
		return err
	}

	var sessions []protocol.SessionInfo
	if err := json.Unmarshal(result, &sessions); err != nil {
		return fmt.Errorf("failed to parse result: %w", err)
	}

	if *jsonOut {
		data, err := json.Marshal(sessions)
		if err != nil {
			return fmt.Errorf("failed to encode result: %w", err)
		}
		fmt.Println(string(data))
		return nil
	}

	if len(sessions) == 0 {
		fmt.Println("No sessions")
		return nil
	}

	fmt.Println("NAME              TYPE     HOST              USER    STATUS")
	for _, s := range sessions {
		fmt.Printf("%-16s %-8s %-17s %-7s %s\n", s.Name, s.Type, s.Host, s.User, s.Status)
	}

	return nil
}

func handleForward(args []string) error {
	fs := flag.NewFlagSet("forward", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)

	name := fs.String("n", "", "Session name")
	local := fs.Int("l", 0, "Local port")
	remote := fs.Int("r", 0, "Remote port")
	isRemote := fs.Bool("R", false, "Remote port forward")
	bindAddr := fs.String("bind", "", "Remote bind address (for -R, default 127.0.0.1)")
	publicBind := fs.Bool("public", false, "Remote bind on 0.0.0.0 (for -R)")

	fs.Parse(args)

	if *local == 0 || *remote == 0 {
		return fmt.Errorf("local and remote ports are required (-l, -r)")
	}

	forwardType := "local"
	resolvedBindAddr := ""
	if *isRemote {
		forwardType = "remote"
		if *publicBind && *bindAddr != "" {
			return fmt.Errorf("--public and --bind cannot be used together")
		}
		if *publicBind {
			resolvedBindAddr = "0.0.0.0"
		} else if *bindAddr != "" {
			resolvedBindAddr = *bindAddr
		} else {
			resolvedBindAddr = "127.0.0.1"
		}
	} else if *publicBind || *bindAddr != "" {
		return fmt.Errorf("--bind/--public are only valid with -R")
	}

	params := protocol.ForwardParams{
		Name:       *name,
		Type:       forwardType,
		LocalPort:  *local,
		RemotePort: *remote,
		BindAddr:   resolvedBindAddr,
	}

	result, err := sendRequest(protocol.MsgForward, params)
	if err != nil {
		return err
	}

	var info protocol.ForwardInfo
	if err := json.Unmarshal(result, &info); err != nil {
		return fmt.Errorf("failed to parse result: %w", err)
	}

	if info.Type == "remote" {
		bind := info.BindAddr
		if bind == "" {
			bind = "127.0.0.1"
		}
		fmt.Printf("Forward started: %s %d:%s:%d (ID: %s)\n", info.Type, info.LocalPort, bind, info.RemotePort, info.ID[:8])
	} else {
		fmt.Printf("Forward started: %s %d:%d (ID: %s)\n", info.Type, info.LocalPort, info.RemotePort, info.ID[:8])
	}
	return nil
}

func handleForwards(args []string) error {
	fs := flag.NewFlagSet("forwards", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	jsonOut := fs.Bool("json", false, "Print result as JSON")
	fs.Parse(args)

	result, err := sendRequest(protocol.MsgForwards, nil)
	if err != nil {
		return err
	}

	var forwards []protocol.ForwardInfo
	if err := json.Unmarshal(result, &forwards); err != nil {
		return fmt.Errorf("failed to parse result: %w", err)
	}

	if *jsonOut {
		data, err := json.Marshal(forwards)
		if err != nil {
			return fmt.Errorf("failed to encode result: %w", err)
		}
		fmt.Println(string(data))
		return nil
	}

	if len(forwards) == 0 {
		fmt.Println("No forwards")
		return nil
	}

	fmt.Println("ID        SESSION         TYPE     BIND            LOCAL  REMOTE")
	for _, f := range forwards {
		bind := "-"
		if f.Type == "remote" {
			bind = f.BindAddr
			if bind == "" {
				bind = "127.0.0.1"
			}
		}
		fmt.Printf("%-9s %-15s %-8s %-15s %-6d %d\n", f.ID[:8], f.Session, f.Type, bind, f.LocalPort, f.RemotePort)
	}

	return nil
}

func handleForwardClose(args []string) error {
	if len(args) < 1 {
		return fmt.Errorf("forward ID required")
	}

	_, err := sendRequest(protocol.MsgForwardClose, protocol.ForwardCloseParams{ID: args[0]})
	if err != nil {
		return err
	}

	fmt.Printf("Forward %s closed\n", args[0])
	return nil
}

func handleSCP(args []string) error {
	fs := flag.NewFlagSet("scp", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)

	name := fs.String("n", "", "Session name")
	isUpload := fs.Bool("put", false, "Upload (local -> remote)")
	isDownload := fs.Bool("get", false, "Download (remote -> local)")

	fs.Parse(args)

	if fs.NArg() < 2 {
		return fmt.Errorf("source and destination paths required")
	}

	if !*isUpload && !*isDownload {
		return fmt.Errorf("must specify -put or -get")
	}

	params := protocol.SCPParams{
		Name:     *name,
		Source:   fs.Arg(0),
		Dest:     fs.Arg(1),
		IsUpload: *isUpload,
	}

	result, err := sendRequest(protocol.MsgSCP, params)
	if err != nil {
		return err
	}

	var transferResult protocol.TransferResult
	if err := json.Unmarshal(result, &transferResult); err != nil {
		return fmt.Errorf("failed to parse result: %w", err)
	}

	if transferResult.Success {
		fmt.Printf("Success: %s\n", transferResult.Message)
	} else {
		return fmt.Errorf("failed: %s", transferResult.Message)
	}

	return nil
}

func handleSFTP(args []string) error {
	fs := flag.NewFlagSet("sftp", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)

	name := fs.String("n", "", "Session name")
	command := fs.String("c", "", "SFTP command (ls, mkdir, rm)")
	path := fs.String("p", "", "Remote path")
	pathAlt := fs.String("d", "", "Remote path (deprecated alias for -p)")

	fs.Parse(args)

	if *command == "" {
		return fmt.Errorf("SFTP command required (-c ls|mkdir|rm)")
	}

	resolvedPath := *path
	if resolvedPath == "" {
		resolvedPath = *pathAlt
	}
	if resolvedPath == "" {
		resolvedPath = "."
	} else if *path != "" && *pathAlt != "" {
		return fmt.Errorf("-p and -d cannot be used together")
	}

	switch *command {
	case "ls":
		params := protocol.SFTPParams{Name: *name, Command: "ls", Path: resolvedPath}
		result, err := sendRequest(protocol.MsgSFTPLs, params)
		if err != nil {
			return err
		}
		var files []string
		if err := json.Unmarshal(result, &files); err != nil {
			return fmt.Errorf("failed to parse result: %w", err)
		}
		for _, f := range files {
			fmt.Println(f)
		}

	case "mkdir":
		params := protocol.SFTPParams{Name: *name, Command: "mkdir", Path: resolvedPath}
		_, err := sendRequest(protocol.MsgSFTPMkdir, params)
		if err != nil {
			return err
		}
		fmt.Printf("Directory created: %s\n", resolvedPath)

	case "rm":
		params := protocol.SFTPParams{Name: *name, Command: "rm", Path: resolvedPath}
		_, err := sendRequest(protocol.MsgSFTPRm, params)
		if err != nil {
			return err
		}
		fmt.Printf("File removed: %s\n", resolvedPath)

	default:
		return fmt.Errorf("unknown SFTP command: %s", *command)
	}

	return nil
}

func handlePing() error {
	_, err := sendRequest(protocol.MsgPing, nil)
	if err != nil {
		return err
	}
	fmt.Println("Daemon is running")
	return nil
}

func handleStop() error {
	_, err := sendRequest(protocol.MsgStop, nil)
	if err != nil {
		return err
	}
	fmt.Println("Daemon stopping")
	return nil
}

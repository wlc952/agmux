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

	port := fs.Int("p", 22, "Port")
	keyPath := fs.String("i", "", "SSH key path")
	password := fs.String("pswd", "", "SSH password (prefer key auth when possible)")
	name := fs.String("n", "", "Session name")

	if err := fs.Parse(args); err != nil {
		return err
	}

	if fs.NArg() != 1 {
		return fmt.Errorf("destination required: gssh connect [flags] user@host")
	}

	user, host, err := parseDestination(fs.Arg(0))
	if err != nil {
		return err
	}

	params := protocol.ConnectParams{
		Name:     *name,
		User:     user,
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

	fmt.Printf("Connected: %s@%s (name: %s, type: %s)\n", info.User, info.Host, info.Name, info.Type)
	return nil
}

func handleLocal(args []string) error {
	fs := flag.NewFlagSet("local", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)

	name := fs.String("n", "", "Session name")
	if err := fs.Parse(args); err != nil {
		return err
	}

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
	password := fs.String("pswd", "", "Password (for reconnecting offline session)")
	keyPath := fs.String("i", "", "SSH key path (for reconnecting offline session)")
	if err := fs.Parse(args); err != nil {
		return err
	}

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
	if err := fs.Parse(args); err != nil {
		return err
	}

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

// printExecResult writes an exec result, either as a single JSON object
// (default, agent-friendly) or as raw streams (--raw), and exits with the
// command's exit code when non-zero.
func printExecResult(result []byte, rawOut bool) error {
	var execResult protocol.ExecResult
	if err := json.Unmarshal(result, &execResult); err != nil {
		return fmt.Errorf("failed to parse result: %w", err)
	}

	if rawOut {
		fmt.Print(execResult.Stdout)
		if execResult.Stderr != "" {
			fmt.Fprintf(os.Stderr, "%s", execResult.Stderr)
		}
	} else {
		data, err := json.Marshal(execResult)
		if err != nil {
			return fmt.Errorf("failed to encode result: %w", err)
		}
		fmt.Println(string(data))
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
	sudoPassword := fs.String("pswd", "", "Sudo password")
	sudoUser := fs.String("sudo-user", "", "Run as specified user")
	sudoLogin := fs.Bool("sudo-login", false, "Login shell (-i)")
	stream := fs.Bool("stream", false, "Stream output in real-time")
	rawOut := fs.Bool("raw", false, "Print raw stdout/stderr instead of JSON")
	_ = fs.Bool("json", false, "(deprecated) JSON is the default; kept for compatibility")

	if err := fs.Parse(args); err != nil {
		return err
	}

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

	return printExecResult(result, *rawOut)
}

func handleRun(args []string) error {
	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)

	timeout := fs.Int("t", 0, "Timeout in seconds")
	useSudo := fs.Bool("sudo", false, "Run with sudo")
	sudoPassword := fs.String("pswd", "", "Sudo password")
	sudoUser := fs.String("sudo-user", "", "Run as specified user")
	sudoLogin := fs.Bool("sudo-login", false, "Login shell (-i)")
	stream := fs.Bool("stream", false, "Stream output in real-time")
	rawOut := fs.Bool("raw", false, "Print raw stdout/stderr instead of JSON")
	_ = fs.Bool("json", false, "(deprecated) JSON is the default; kept for compatibility")

	if err := fs.Parse(args); err != nil {
		return err
	}

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

	return printExecResult(result, *rawOut)
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
	if err := fs.Parse(args); err != nil {
		return err
	}

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
	localSpec := fs.String("L", "", "Local forward: localPort:remotePort (like ssh -L)")
	remoteSpec := fs.String("R", "", "Remote forward: remotePort:localPort (like ssh -R)")
	bindAddr := fs.String("bind", "", "Remote bind address (for -R, default 127.0.0.1)")
	publicBind := fs.Bool("public", false, "Remote bind on 0.0.0.0 (for -R)")

	if err := fs.Parse(args); err != nil {
		return err
	}

	sessionName := *name

	if *localSpec != "" && *remoteSpec != "" {
		return fmt.Errorf("-L and -R cannot be used together")
	}
	if *localSpec == "" && *remoteSpec == "" {
		return fmt.Errorf("forward spec required: -L localPort:remotePort or -R remotePort:localPort")
	}

	forwardType := "local"
	spec := *localSpec
	if *remoteSpec != "" {
		forwardType = "remote"
		spec = *remoteSpec
	}

	first, second, err := parseForwardSpec(spec)
	if err != nil {
		return err
	}

	var localPort, remotePort int
	resolvedBindAddr := ""
	if forwardType == "local" {
		localPort, remotePort = first, second
		if *publicBind || *bindAddr != "" {
			return fmt.Errorf("--bind/--public are only valid with -R")
		}
	} else {
		// ssh -R order: remotePort:localPort
		remotePort, localPort = first, second
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
	}

	params := protocol.ForwardParams{
		Name:       sessionName,
		Type:       forwardType,
		LocalPort:  localPort,
		RemotePort: remotePort,
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
	if err := fs.Parse(args); err != nil {
		return err
	}

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
	if err := fs.Parse(args); err != nil {
		return err
	}

	if fs.NArg() < 2 {
		return fmt.Errorf("source and destination required: gssh scp <src> <dst> (one side session:path)")
	}

	srcSess, srcPath, srcRemote := splitRemoteSpec(fs.Arg(0))
	dstSess, dstPath, dstRemote := splitRemoteSpec(fs.Arg(1))

	if srcRemote == dstRemote {
		return fmt.Errorf("exactly one side must be remote (session:path)")
	}
	if srcRemote && srcPath == "." {
		// `gssh scp prod: ./dst` would recursively pull the entire remote
		// home directory — almost certainly a mistake.
		return fmt.Errorf("empty remote path: specify one explicitly (e.g. %s:/var/log/app.log)", srcSess)
	}

	params := protocol.SCPParams{
		Name:     dstSess,
		Source:   fs.Arg(0),
		Dest:     dstPath,
		IsUpload: true,
	}
	if srcRemote {
		// Download: session:path -> local
		params = protocol.SCPParams{
			Name:     srcSess,
			Source:   srcPath,
			Dest:     fs.Arg(1),
			IsUpload: false,
		}
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

	if err := fs.Parse(args); err != nil {
		return err
	}

	if fs.NArg() < 1 {
		return fmt.Errorf("SFTP command required: gssh sftp [-n session] ls|mkdir|rm <path>")
	}

	command := fs.Arg(0)
	path := fs.Arg(1)
	if path == "" {
		if command == "ls" {
			path = "."
		} else {
			return fmt.Errorf("path required: gssh sftp [-n session] %s <path>", command)
		}
	}

	switch command {
	case "ls":
		params := protocol.SFTPParams{Name: *name, Command: "ls", Path: path}
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
		params := protocol.SFTPParams{Name: *name, Command: "mkdir", Path: path}
		_, err := sendRequest(protocol.MsgSFTPMkdir, params)
		if err != nil {
			return err
		}
		fmt.Printf("Directory created: %s\n", path)

	case "rm":
		params := protocol.SFTPParams{Name: *name, Command: "rm", Path: path}
		_, err := sendRequest(protocol.MsgSFTPRm, params)
		if err != nil {
			return err
		}
		fmt.Printf("File removed: %s\n", path)

	default:
		return fmt.Errorf("unknown SFTP command: %s (want ls, mkdir, or rm)", command)
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

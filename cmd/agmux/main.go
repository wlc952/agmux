package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"agmux/internal/protocol"
	"agmux/pkg/imsg"
)

var (
	socketPath string
	version    = "dev"
)

func main() {
	// Parse global -S flag first
	args := os.Args[1:]
	socketPath = defaultSocketPath()
	for i := 0; i < len(args); i++ {
		if args[i] == "-S" && i+1 < len(args) {
			socketPath = args[i+1]
			args = append(args[:i], args[i+2:]...)
			i--
		}
	}

	if len(args) > 0 && (args[0] == "-v" || args[0] == "--version") {
		fmt.Printf("agmux version %s\n", version)
		os.Exit(0)
	}

	if len(args) > 0 && (args[0] == "-h" || args[0] == "--help") {
		printUsage()
		os.Exit(0)
	}

	if len(args) < 1 {
		printUsage()
		os.Exit(1)
	}

	cmd := args[0]
	subArgs := args[1:]

	var err error
	switch cmd {
	case "start":
		err = handleStart()
	case "connect":
		err = handleConnect(subArgs)
	case "local":
		err = handleLocal(subArgs)
	case "detach":
		err = handleDetach(subArgs)
	case "kill":
		err = handleKill(subArgs)
	case "attach":
		err = handleAttach(subArgs)
	case "exec":
		err = handleExec(subArgs)
	case "run":
		err = handleRun(subArgs)
	case "list", "ls":
		err = handleList()
	case "use":
		err = handleUse(subArgs)
	case "forward":
		err = handleForward(subArgs)
	case "forwards":
		err = handleForwards()
	case "forward-close":
		err = handleForwardClose(subArgs)
	case "scp", "sync":
		err = handleSCP(subArgs)
	case "sftp":
		err = handleSFTP(subArgs)
	case "reconnect":
		err = handleReconnect(subArgs)
	case "ping":
		err = handlePing()
	case "stop":
		err = handleStop()
	case "help":
		printUsage()
	default:
		fmt.Fprintf(os.Stderr, "Unknown command: %s\n", cmd)
		printUsage()
		os.Exit(1)
	}

	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
}

func sendRequest(method uint16, params interface{}) ([]byte, error) {
	conn, err := net.Dial("unix", socketPath)
	if err != nil {
		return nil, fmt.Errorf("failed to connect to daemon (is it running?): %w", err)
	}
	defer conn.Close()

	var payload []byte
	if params != nil {
		payload, err = protocol.EncodePayload(params)
		if err != nil {
			return nil, fmt.Errorf("failed to encode params: %w", err)
		}
	} else {
		payload = []byte{}
	}

	msg := imsg.NewImsg(method, payload)
	if err := imsg.WriteMessage(conn, msg); err != nil {
		return nil, fmt.Errorf("failed to send request: %w", err)
	}

	resp, err := imsg.ReadMessage(conn)
	if err != nil {
		return nil, fmt.Errorf("failed to read response: %w", err)
	}

	if resp.Type == protocol.MsgError {
		var errPayload protocol.ErrorPayload
		if err := json.Unmarshal(resp.Payload, &errPayload); err != nil {
			return nil, fmt.Errorf("RPC error (unparseable)")
		}
		return nil, fmt.Errorf("RPC error: %s", errPayload.Message)
	}

	return resp.Payload, nil
}

func handleStart() error {
	// Check if daemon is already running
	conn, err := net.Dial("unix", socketPath)
	if err == nil {
		conn.Close()
		fmt.Println("Daemon is already running")
		return nil
	}

	// Fork daemon process
	serverBin, err := findServerBinary()
	if err != nil {
		return fmt.Errorf("cannot find agmux-server binary: %w", err)
	}

	// Start daemon in background
	cmd := exec.Command(serverBin, "-S", socketPath)
	cmd.Stdout = os.Stderr
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("failed to start daemon: %w", err)
	}

	fmt.Printf("Daemon started (PID: %d, socket: %s)\n", cmd.Process.Pid, socketPath)
	return nil
}

func handleConnect(args []string) error {
	fs := flag.NewFlagSet("connect", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)

	name := fs.String("n", "", "Session name")
	user := fs.String("u", "", "Username")
	host := fs.String("h", "", "Host")
	port := fs.Int("p", 22, "Port")
	password := fs.String("P", "", "Password")
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

	result, err := sendRequest(protocol.MsgConnect, params)
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

func handleDetach(args []string) error {
	fs := flag.NewFlagSet("detach", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	name := fs.String("n", "", "Session name")
	fs.Parse(args)

	_, err := sendRequest(protocol.MsgDetach, protocol.DetachParams{Name: *name})
	if err != nil {
		return err
	}

	fmt.Println("Detached")
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

func handleAttach(args []string) error {
	fs := flag.NewFlagSet("attach", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)

	name := fs.String("n", "", "Session name")
	password := fs.String("P", "", "Password (for reconnecting)")
	keyPath := fs.String("i", "", "SSH key path (for reconnecting)")
	fs.Parse(args)

	_, err := sendRequest(protocol.MsgAttach, protocol.AttachParams{
		Name:     *name,
		Password: *password,
		KeyPath:  *keyPath,
	})
	if err != nil {
		return err
	}

	fmt.Println("Attached")
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

	fs.Parse(args)

	if fs.NArg() < 1 {
		return fmt.Errorf("command required")
	}
	command := strings.Join(fs.Args(), "")

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

	result, err := sendRequest(protocol.MsgExec, params)
	if err != nil {
		return err
	}

	var execResult protocol.ExecResult
	if err := json.Unmarshal(result, &execResult); err != nil {
		return fmt.Errorf("failed to parse result: %w", err)
	}

	fmt.Print(execResult.Stdout)
	if execResult.Stderr != "" {
		fmt.Fprintf(os.Stderr, "%s", execResult.Stderr)
	}

	if execResult.ExitCode != 0 {
		os.Exit(execResult.ExitCode)
	}

	return nil
}

func handleRun(args []string) error {
	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)

	timeout := fs.Int("t", 0, "Timeout in seconds")
	useSudo := fs.Bool("sudo", false, "Run with sudo")
	sudoPassword := fs.String("sudo-password", "", "Sudo password")
	sudoUser := fs.String("sudo-user", "", "Run as specified user")
	sudoLogin := fs.Bool("sudo-login", false, "Login shell (-i)")

	fs.Parse(args)

	if fs.NArg() < 1 {
		return fmt.Errorf("command required")
	}
	command := strings.Join(fs.Args(), " ")

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

	result, err := sendRequest(protocol.MsgLocalExec, params)
	if err != nil {
		return err
	}

	var execResult protocol.ExecResult
	if err := json.Unmarshal(result, &execResult); err != nil {
		return fmt.Errorf("failed to parse result: %w", err)
	}

	fmt.Print(execResult.Stdout)
	if execResult.Stderr != "" {
		fmt.Fprintf(os.Stderr, "%s", execResult.Stderr)
	}

	if execResult.ExitCode != 0 {
		os.Exit(execResult.ExitCode)
	}

	return nil
}

func handleList() error {
	result, err := sendRequest(protocol.MsgList, nil)
	if err != nil {
		return err
	}

	var sessions []protocol.SessionInfo
	if err := json.Unmarshal(result, &sessions); err != nil {
		return fmt.Errorf("failed to parse result: %w", err)
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

func handleUse(args []string) error {
	if len(args) < 1 {
		return fmt.Errorf("session name required")
	}

	_, err := sendRequest(protocol.MsgUse, protocol.UseParams{Name: args[0]})
	if err != nil {
		return err
	}

	fmt.Printf("Using session: %s\n", args[0])
	return nil
}

func handleForward(args []string) error {
	fs := flag.NewFlagSet("forward", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)

	name := fs.String("n", "", "Session name")
	local := fs.Int("l", 0, "Local port")
	remote := fs.Int("r", 0, "Remote port")
	isRemote := fs.Bool("R", false, "Remote port forward")

	fs.Parse(args)

	if *local == 0 || *remote == 0 {
		return fmt.Errorf("local and remote ports are required (-l, -r)")
	}

	forwardType := "local"
	if *isRemote {
		forwardType = "remote"
	}

	params := protocol.ForwardParams{
		Name:       *name,
		Type:       forwardType,
		LocalPort:  *local,
		RemotePort: *remote,
	}

	result, err := sendRequest(protocol.MsgForward, params)
	if err != nil {
		return err
	}

	var info protocol.ForwardInfo
	if err := json.Unmarshal(result, &info); err != nil {
		return fmt.Errorf("failed to parse result: %w", err)
	}

	fmt.Printf("Forward started: %s %d:%d (ID: %s)\n", info.Type, info.LocalPort, info.RemotePort, info.ID[:8])
	return nil
}

func handleForwards() error {
	result, err := sendRequest(protocol.MsgForwards, nil)
	if err != nil {
		return err
	}

	var forwards []protocol.ForwardInfo
	if err := json.Unmarshal(result, &forwards); err != nil {
		return fmt.Errorf("failed to parse result: %w", err)
	}

	if len(forwards) == 0 {
		fmt.Println("No forwards")
		return nil
	}

	fmt.Println("ID        SESSION         TYPE     LOCAL  REMOTE")
	for _, f := range forwards {
		fmt.Printf("%-9s %-15s %-8s %-6d %d\n", f.ID[:8], f.Session, f.Type, f.LocalPort, f.RemotePort)
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
	path := fs.String("p", ".", "Path")

	fs.Parse(args)

	if *command == "" {
		return fmt.Errorf("SFTP command required (-c ls|mkdir|rm)")
	}

	switch *command {
	case "ls":
		params := protocol.SFTPParams{Name: *name, Command: "ls", Path: *path}
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
		params := protocol.SFTPParams{Name: *name, Command: "mkdir", Path: *path}
		_, err := sendRequest(protocol.MsgSFTPMkdir, params)
		if err != nil {
			return err
		}
		fmt.Printf("Directory created: %s\n", *path)

	case "rm":
		params := protocol.SFTPParams{Name: *name, Command: "rm", Path: *path}
		_, err := sendRequest(protocol.MsgSFTPRm, params)
		if err != nil {
			return err
		}
		fmt.Printf("File removed: %s\n", *path)

	default:
		return fmt.Errorf("unknown SFTP command: %s", *command)
	}

	return nil
}

func handleReconnect(args []string) error {
	fs := flag.NewFlagSet("reconnect", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	name := fs.String("n", "", "Session name")
	fs.Parse(args)

	_, err := sendRequest(protocol.MsgReconnect, protocol.ReconnectParams{Name: *name})
	if err != nil {
		return err
	}

	fmt.Println("Reconnected")
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

func findServerBinary() (string, error) {
	// Try same directory as agmux binary
	self, err := os.Executable()
	if err != nil {
		return "", err
	}
	dir := filepath.Dir(self)
	serverPath := filepath.Join(dir, "agmux-server")
	if _, err := os.Stat(serverPath); err == nil {
		return serverPath, nil
	}

	// Try /usr/local/bin
	serverPath = "/usr/local/bin/agmux-server"
	if _, err := os.Stat(serverPath); err == nil {
		return serverPath, nil
	}

	return "", fmt.Errorf("agmux-server binary not found")
}

func defaultSocketPath() string {
	return "/tmp/agmux.sock"
}

func printUsage() {
	fmt.Printf(`agmux - Agent Multiplexer v%s

Usage:
  agmux start                                    Start daemon
  agmux connect -u user -h host [-n name] [-p port] [-P password] [-i key]
  agmux local [-n name]                          Create local session
  agmux attach [-n name] [-P password] [-i key]  Attach to session
  agmux detach [-n name]                         Detach (SSH stays alive)
  agmux kill [-n name]                           Kill session
  agmux exec [-n name] [-t timeout] [--sudo ...] "command"
  agmux run [-t timeout] [--sudo ...] "command"  One-off local exec
  agmux list | ls                                List sessions
  agmux use <name>                               Set default session
  agmux forward [-n name] -l local -r remote [-R]
  agmux forwards                                 List forwards
  agmux forward-close <id>
  agmux scp [-n name] -put|-get <src> <dst>
  agmux sync [-n name] -put|-get <src> <dst>
  agmux sftp [-n name] -c ls|mkdir|rm -p <path>
  agmux reconnect [-n name]
  agmux ping                                     Check daemon
  agmux stop                                     Stop daemon
  agmux -v, --version

Options:
  -S socket_path    Unix socket path (default: /tmp/agmux.sock)
  -n name           Session name
  -t timeout        Command timeout in seconds
  --sudo            Run with sudo
  --sudo-password   Sudo password
  --sudo-user       Run as specified user
  --sudo-login      Login shell (-i)

Examples:
  agmux connect -u admin -h 10.0.1.1 -n production -P password
  agmux exec -n production "ls -la"
  agmux exec --sudo --sudo-password 1234 "ls /root/"
  agmux run "ls -la /tmp"
  agmux detach -n production
  agmux attach -n production
  agmux forward -n production -l 8080 -r 80
`, version)
}
package main

import (
	"fmt"
	"os"

	"gssh/internal/socketpath"
)

var (
	socketPath string
	version    = "dev"

	// allowAutoStart controls whether CLI commands transparently spawn the
	// daemon when its socket is missing or stale. Lifecycle commands
	// (start/stop/ping/server) disable it.
	allowAutoStart = true
)

func main() {
	// Parse global -S flag first
	args := os.Args[1:]
	socketPath = socketpath.Default()
	for i := 0; i < len(args); i++ {
		if args[i] == "-S" && i+1 < len(args) {
			socketPath = args[i+1]
			args = append(args[:i], args[i+2:]...)
			i--
		}
	}

	if len(args) > 0 && (args[0] == "-v" || args[0] == "--version") {
		fmt.Printf("gssh version %s\n", version)
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

	// Lifecycle commands must not implicitly start the daemon.
	switch cmd {
	case "start", "stop", "ping", "server":
		allowAutoStart = false
	}

	var err error
	switch cmd {
	case "start":
		err = handleStart()
	case "server":
		err = runDaemon(subArgs)
	case "connect":
		err = handleConnect(subArgs)
	case "local":
		err = handleLocal(subArgs)
	case "kill":
		err = handleKill(subArgs)
	case "exec":
		err = handleExec(subArgs)
	case "run":
		err = handleRun(subArgs)
	case "list", "ls":
		err = handleList(subArgs)
	case "use":
		err = handleUse(subArgs)
	case "forward":
		err = handleForward(subArgs)
	case "forwards":
		err = handleForwards(subArgs)
	case "forward-close":
		err = handleForwardClose(subArgs)
	case "scp", "sync":
		err = handleSCP(subArgs)
	case "sftp":
		err = handleSFTP(subArgs)
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

func printUsage() {
	fmt.Printf(`gssh - SSH session multiplexer for agents v%s

Usage:
  gssh start                                     Start daemon (auto-started on demand)
  gssh connect -u user -h host [-n name] [-P port] [-p password] [-i key]
  gssh local [-n name]                           Create local session
  gssh kill [-n name]                            Kill session
  gssh exec [-n name] [-t timeout] [--stream] [--json] [--sudo ...] "command"
  gssh run [-t timeout] [--stream] [--json] [--sudo ...] "command"  One-off local exec
  gssh list | ls [--json]                        List sessions
  gssh use <name> [-p password] [-i key]         Switch default / reconnect session
  gssh forward [-n name] -l local -r remote [-R] [--bind addr|--public]
  gssh forwards [--json]                         List forwards
  gssh forward-close <id>                        Close forward (ID or unique prefix)
  gssh scp [-n name] -put|-get <src> <dst>
  gssh sync [-n name] -put|-get <src> <dst>
  gssh sftp [-n name] -c ls|mkdir|rm -p <path>
  gssh ping                                      Check daemon
  gssh stop                                      Stop daemon
  gssh server                                    Run daemon in foreground (internal)
  gssh -v, --version

Options:
  -S socket_path    Unix socket path (default: %s)
  -n name           Session name
  -u user           Username
  -h host           Host address
  -P port           SSH port (default: 22)
  -p password       SSH password (for sftp: remote path)
  -i key_path       SSH key path
  -t timeout        Command timeout in seconds
  --sudo            Run with sudo
  --sudo-password   Sudo password
  --sudo-user       Run as specified user
  --sudo-login      Login shell (-i)
  --stream          Stream stdout/stderr in real-time (for long-running commands)
  --json            Machine-readable JSON output
  --bind addr       Remote bind address for -R (default: 127.0.0.1)
  --public          Shortcut for --bind 0.0.0.0

The daemon starts automatically when any command needs it; "gssh start" is
only needed to control startup explicitly. Daemon logs go to ~/.gssh/server.log.

Examples:
  gssh connect -u admin -h 10.0.1.1 -n production -p password
  gssh exec -n production "ls -la"
  gssh exec --json -t 10 "make build"
  gssh exec --sudo --sudo-password 1234 "ls /root/"
  gssh run "ls -la /tmp"
  gssh use production -p password
  gssh forward -n production -l 8080 -r 80
`, version, socketpath.Default())
}

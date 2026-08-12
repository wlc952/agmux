# gssh - Architecture Design

gssh is a session management and command execution tool for AI agents.
Inspired by tmux's client-server architecture, it manages both SSH and local
sessions, providing session switching with reconnect, structured output, and
automatic reconnect.

Core constraint: **fully non-interactive and scriptable — no TTY anywhere.**

## 1. Architecture Overview

Single binary, tmux-style. The same `gssh` binary acts as thin client or as
the daemon (`gssh server`). The daemon is started on demand: any command that
needs it spawns it automatically when the socket is missing or stale.

```
┌──────────────┐  Unix Socket (0600)  ┌──────────────────┐
│  gssh <cmd>  │ ──────────────────── │  gssh server     │
│  thin client │  imsg binary protocol│  (daemon)        │
└──────────────┘                      │                  │
      │  auto-spawn if not running    │  Session Manager │
      └─────────────────────────────▶ │  ┌──────────────┤
                                      │  │ SSH Session  │────── SSH tunnel
                                      │  │ Local Session│────── local exec
                                      │  └──────────────┤
                                      │  Services:      │
                                      │  ExecService    │
                                      │  ForwardService │
                                      │  TransferService│
                                      │  ReconnectMon   │
                                      │  AuditLogger    │
                                      └──────────────────┘
```

Daemon lifecycle:

- **Auto-start**: the CLI's dial path (`dialDaemon`) detects a missing
  (`ENOENT`) or stale (`ECONNREFUSED`) socket, spawns `gssh server -S <sock>`
  detached (setsid, stdin from /dev/null, stdout/stderr appended to
  `~/.gssh/server.log`), and polls until the socket accepts connections
  (5s timeout). Lifecycle commands (`start`, `stop`, `ping`, `server`) never
  auto-start.
- **Explicit start**: `gssh start` runs the same spawn path and waits for
  readiness, so `gssh start && gssh connect ...` cannot race.
- **Stop**: `gssh stop` sends MsgStop; the server runs the graceful shutdown
  sequence and the daemon process exits (Start returning ends `runDaemon` —
  including when shutdown was triggered by RPC rather than a signal).

## 2. Directory Structure

```
gssh/
├── cmd/
│   └── gssh/
│       ├── main.go               # Entry point, dispatch (incl. user@host shorthand), usage
│       ├── client.go             # RPC client: dial, auto-start, streaming
│       ├── server.go             # `gssh server` subcommand (daemon)
│       ├── commands.go           # Subcommand flag parsing + handlers
│       ├── dest.go               # ssh-style destination/scp-endpoint/forward-spec parsing
│       ├── spawn_unix.go         # setsid detach (//go:build unix)
│       ├── spawn_other.go        # no-op detach (//go:build !unix)
│       ├── peercred_darwin.go    # Peer credential check (LOCAL_PEERCRED)
│       ├── peercred_linux.go     # Peer credential check (SO_PEERCRED)
│       └── peercred_other.go     # No-op on other platforms
├── internal/
│   ├── protocol/
│   │   ├── msg.go                # Message type constants
│   │   └── types.go              # Request/response structs + JSON helpers
│   ├── server/
│   │   ├── server.go             # Unix socket listener, dispatch, streaming
│   │   └── server_test.go
│   ├── session/
│   │   ├── session.go            # Session iface, SSHSession, LocalSession, Manager
│   │   └── session_test.go
│   ├── ssh/
│   │   ├── client.go             # Dial, auth (pswd/-i/agent/default keys), known_hosts
│   │   └── client_test.go
│   ├── exec/
│   │   ├── executor.go           # remote/local × buffered/stream × sudo matrix
│   │   └── executor_test.go
│   ├── shellquote/
│   │   ├── quote.go              # POSIX single-quote escaping
│   │   └── quote_test.go
│   ├── portforward/
│   │   ├── forwarder.go          # Forwarder + Service (registry, lifecycle)
│   │   └── forwarder_test.go
│   ├── transfer/
│   │   ├── sftp.go               # SFTP upload/download/dir ops
│   │   ├── safefile_unix.go      # TOCTOU-safe download writes (fd-anchored O_NOFOLLOW)
│   │   ├── safefile_other.go     # Portable fallback (path-traversal checks only)
│   │   └── sftp_test.go
│   ├── reconnect/
│   │   ├── monitor.go            # Health check + exponential backoff
│   │   └── monitor_test.go
│   ├── persist/
│   │   ├── store.go              # ~/.gssh/state.json save/load
│   │   └── store_test.go
│   ├── audit/
│   │   ├── log.go                # ~/.gssh/audit.log JSON-lines
│   │   └── log_test.go
│   └── socketpath/
│       ├── path.go               # Socket path resolution + validation
│       ├── owner_unix.go         # UID ownership (unix)
│       └── owner_windows.go
├── pkg/
│   └── imsg/
│       ├── imsg.go               # Binary frame read/write
│       └── imsg_test.go
├── Makefile
├── DESIGN.md
└── README.md
```

## 3. Protocol Design (imsg)

### Message Format

Binary frame with JSON payload. Human-debuggable, type-safe framing.

```
+----------+----------+----------+----------+----------+
| Version  | Type     | Length   |  Payload ...          |
| uint8    | uint16   | uint32   |  (Length bytes)       |
+----------+----------+----------+----------+----------+
  1 byte     2 bytes    4 bytes     variable
```

- **Version** (uint8): Protocol version, currently 1
- **Type** (uint16): Message type constant, big-endian
- **Length** (uint32): Payload length in bytes, big-endian, max 4 MiB
- **Payload**: JSON-encoded request params or response result

### Message Types

```go
// Client → Server (requests)
const (
    MsgConnect         uint16 = 1
    MsgLocal           uint16 = 2
    // 3 (MsgDetach) and 5 (MsgAttach) removed — vacant, do not reuse
    MsgKill            uint16 = 4
    MsgExec            uint16 = 6
    MsgLocalExec       uint16 = 7
    MsgList            uint16 = 8
    MsgUse             uint16 = 9
    MsgForward         uint16 = 10
    MsgForwards        uint16 = 11
    MsgForwardClose    uint16 = 12
    MsgSCP             uint16 = 13
    MsgSFTPLs          uint16 = 14
    MsgSFTPMkdir       uint16 = 15
    MsgSFTPRm          uint16 = 16
    // 17 (MsgReconnect) removed — use MsgUse with credentials instead
    MsgPing            uint16 = 18
    MsgStop            uint16 = 19
    MsgExecStream      uint16 = 20
    MsgLocalExecStream uint16 = 21
)

// Server → Client (responses)
const (
    MsgResult      uint16 = 100
    MsgError       uint16 = 101
    MsgPong        uint16 = 102
    MsgStreamChunk uint16 = 103
    MsgStreamEnd   uint16 = 104
)
```

### Request/response flow

Normal messages follow strict request → single-response. Streaming exec
messages (MsgExecStream, MsgLocalExecStream) bypass the dispatch loop: the
handler writes any number of MsgStreamChunk frames followed by exactly one
MsgStreamEnd carrying the exit code.

Responses larger than the 4 MiB frame cap are rejected with a clear MsgError
telling the caller to re-run with `--stream`, instead of truncating the
connection mid-write.

### Imsg API

```go
// pkg/imsg/imsg.go
type Imsg struct {
    Version uint8
    Type    uint16
    Payload []byte
}

func ReadMessage(r io.Reader) (*Imsg, error)
func WriteMessage(w io.Writer, msg *Imsg) error
```

## 4. Server Architecture

### Core (internal/server/server.go)

```go
type Server struct {
    socketPath string
    listener   net.Listener
    sessions   *session.Manager
    executor   *exec.Executor
    forwards   *portforward.Service
    transfer   *transfer.Service
    reconnect  *reconnect.Monitor
    persist    *persist.Store
    audit      *audit.Logger
    ...
}

func (s *Server) Start() error   // EnsureParentDir → remove stale socket → listen → chmod 0600 → restoreState → accept loop
func (s *Server) Stop() error    // Idempotent graceful shutdown
```

### Graceful Shutdown Sequence

1. Close listener (reject new connections)
2. Persist session state to `~/.gssh/state.json`
3. Kill all sessions (close SSH connections)
4. Drain active connection handlers (poll-close until waitgroup empties)
5. Remove socket file, close audit log

### State Restore (on Start)

- Load `~/.gssh/state.json`.
- Local sessions: re-created immediately.
- SSH sessions: registered as `offline`. Unless the session used password-only
  auth (`auto_reconnect` in state), a background goroutine reconnects via
  `Manager.Reconnect` — explicit key path if present, otherwise the default
  key material (agent / default key files) — and starts the reconnect monitor.
  Password-only sessions stay offline (passwords are never persisted) until
  the agent runs `gssh use <name> --pswd password`.
- `Reconnect` deliberately does not steal the default session.

## 5. Session Model

All session code lives in `internal/session/session.go`.

```go
type Session interface {
    GetName() string
    GetType() string // "ssh" or "local"
    GetHost() string
    GetUser() string
    GetStatus() Status
    SetStatus(Status)
    Close() error
    IsLocal() bool
    GetCreatedAt() time.Time
    GetLastCmd() string
    SetLastCmd(cmd string)
}
```

### Status Lifecycle

```
connecting → connected → disconnected (after kill)
                       → reconnecting → connected / offline
                       → offline → (use -p/-i or restore auto-reconnect → connected)
```

### Manager

`Manager` owns `map[string]Session` plus a default-session name. Key
behaviors:

- `ConnectSSH` dedups on name: same user+host+port returns the existing
  session; same name with a different host is rejected.
- New SSH connections dial **outside** the manager lock; state is re-validated
  after the dial to avoid racing kill/use.
- Concurrency invariants (do not break these):
  - A dedup hit on a `connecting`/`reconnecting` session **waits** via
    `SSHSession.AwaitClient` instead of returning a client-less session or
    double-dialing.
  - All reconnect paths (`Use`, restore, monitor, offline `ConnectSSH`)
    funnel through `Manager.reconnectSSH`, which holds the per-session
    `reconnMu` for the whole dial-and-adopt cycle and re-checks registration
    + status after the dial. A kill landing mid-dial closes the new client
    instead of leaking it.
  - Callers that need the SSH client (exec, transfer, portforward) use
    `AwaitClient`, so a command fired while a (re)connect is in flight waits
    for the outcome rather than failing with "session not connected".
  - The dial func is injectable (`Manager.SetConnectFunc`) for tests.
- `Use(name, password, keyPath)` sets the default and, for offline SSH
  sessions, updates credentials and reconnects.
- `Reconnect(name)` reconnects without touching the default (state restore).

### Naming Rules

- SSH session auto-name: `{user}@{host}`; local auto-name: `local`.
- Only one local session per name; local sessions never reconnect/forward/transfer.

## 6. Command Execution

`internal/exec/executor.go` holds the full matrix:
remote/local × buffered/stream × normal/sudo.

- Commands are wrapped as `/bin/bash -c <shellquote.Quote(command)>`
  (locally executed via `/bin/sh -c`). Quoting uses POSIX single-quote
  escaping (`internal/shellquote`), **not** Go's `%q` — `%q` corrupts
  newlines and control characters inside the command string.
- No PTY; stdout/stderr are separate pipes in all modes.
- Timeout: goroutine + timer; on timeout the process group is SIGKILLed and
  the result is exit code **-1** plus a "command timed out" error.
- A non-zero exit status is not a Go error — it is returned as `ExitCode`.

### Sudo (safe, no shell injection)

```
sudo [-i | -u <quoted user>] -S -p '' /bin/bash -c '<quoted command>'
```

The password is written to stdin (`-S`) and stdin is closed; passwords are
never concatenated into the command line. The sudo user name is shell-quoted.

## 7. Port Forwarding

`internal/portforward/forwarder.go` holds both the `Forwarder` (one forward)
and the `Service` (registry keyed by full UUID).

- Local forward: `localhost:localPort → SSH tunnel → 127.0.0.1:remotePort` (remote side).
- Remote forward: `bindAddr:remotePort` on the server → `127.0.0.1:localPort` locally;
  bind defaults to `127.0.0.1`, `--public` selects `0.0.0.0`.
- `Close` accepts a full ID or an unambiguous prefix (the CLI prints 8-char
  prefixes); ambiguous prefixes are rejected.
- `RestartAll(sessionName, newClient)` re-creates listeners after reconnect;
  local listeners re-bind, remote listeners re-Listen on the new connection.

## 8. File Transfer

SFTP (`github.com/pkg/sftp`) over the session's SSH connection; SSH sessions
only. Upload/download recurse into directories; `sync` semantics skip files
with identical size+mtime.

Download-side hardening is split by platform (`safefile_unix.go` /
`safefile_other.go` behind a `safeRoot` type): on unix, all writes are
fd-anchored to the destination root with `O_NOFOLLOW` traversal (immune to
symlink-swap races); elsewhere, plain path-based writes with the same
`..` traversal rejection. Both reject paths escaping the destination root.

## 9. Reconnect Monitor

`internal/reconnect/monitor.go`:

- Watches each connected SSH session; health check every 5s via
  `keepalive@gssh` SSH request with a 5s timeout.
- On failure: delegates to `Manager.Reconnect` (serialized via `reconnMu`,
  redials with stored credentials). Backoff: 5s → 10s → 20s → ... → max 5min,
  reset on success.
- On success: status → `connected`, then `forwards.RestartAll`.
- `kill` stops the watch; `use` on an offline session re-arms it.

## 10. State Persistence

`~/.gssh/state.json` (0600), written on graceful shutdown:

```go
type SessionState struct {
    Name          string `json:"name"`
    Type          string `json:"type"` // "ssh" or "local"
    Host          string `json:"host"`
    User          string `json:"user"`
    Port          int    `json:"port,omitempty"`
    KeyPath       string `json:"key_path,omitempty"`
    Status        string `json:"status"`
    CreatedAt     int64  `json:"created_at"`
    AutoReconnect bool   `json:"auto_reconnect,omitempty"` // false only for password-only auth
    // Password deliberately NOT persisted
}
```

## 11. Audit Logging

Every action (connect, use, kill, exec, forward, transfer, ...) is appended
as one JSON line to `~/.gssh/audit.log` (0600). Result is `success`, `error`,
or `exit_<code>` for exec.

## 12. Security

1. **Socket auth**: Unix socket 0600 under a per-user directory; both client
   and server validate ownership and permissions (`internal/socketpath`).
2. **Peer credentials**: the client verifies the daemon's UID via
   `LOCAL_PEERCRED` (macOS) / `SO_PEERCRED` (Linux); no-op elsewhere.
3. **Sudo safety**: password via `sudo -S` stdin only; usernames shell-quoted.
4. **Shell quoting**: all command wrapping uses POSIX single-quote escaping;
   no string is ever embedded raw into a shell command line.
5. **Password handling**: never persisted; memory only for session lifetime.
6. **Path traversal**: `filepath.IsLocal()` check for SFTP downloads.
7. **Host keys**: `known_hosts` enforced; unknown keys rejected unless
   `GSSH_INSECURE_ACCEPT_NEW_HOST_KEYS=1` (TOFU opt-in); mismatches always rejected.

## 13. CLI Design (ssh-aligned)

The CLI mirrors ssh/scp muscle memory. The `connect` subcommand is optional:
any first argument containing `@` is treated as a destination.

```
gssh user@host [-p port] [-i key] [--pswd pw] [-n name]     # connect (session created)
gssh user@host [flags] command...                           # connect-if-needed + exec
gssh connect user@host [-p port] [-i key] [--pswd pw]       # explicit form

gssh local [-n name]
gssh exec [-n name] [-t timeout] [--stream] [--raw] [--sudo ...] "command"
gssh run [-t timeout] [--stream] [--raw] [--sudo ...] "command"
gssh list | ls [--json]
gssh use <name> [--pswd pw] [-i key]
gssh kill [-n name]
gssh forward [-n name] -L localPort:remotePort              # ssh -L semantics
gssh forward [-n name] -R remotePort:localPort [--bind addr|--public]
gssh forwards [--json]
gssh forward-close <id-or-prefix>
gssh scp|sync <src> <dst>                                   # one side is session:path
gssh sftp [-n name] ls|mkdir|rm <path>
gssh ping
gssh start | stop
gssh server                                                 # daemon in foreground (internal)
gssh -v, --version
gssh -S <socket_path> ...                                   # global, before the subcommand
```

Shorthand exec (`gssh user@host "cmd"`) is client-side composition: MsgConnect
(dedup fast-path reuses a live session) followed by MsgExec/MsgExecStream —
no new protocol surface.

Output modes (agent-first): non-streaming exec prints a single JSON object
`{"stdout","stderr","exit_code"}` to stdout by default and the CLI process
exits with the command's exit code, so shell `&&`-chains keep working.
`--raw` switches to raw stdout/stderr passthrough for humans and pipes.
`--json` is accepted as a no-op for compatibility with pre-default-JSON
scripts. `--stream` bypasses result framing entirely (chunk frames).

Parsing notes:

- Destination: split at the **last** `@`; a missing user defaults to the
  current OS user (like ssh). The shorthand gate requires `@` so session
  names can never collide with destinations.
- scp endpoints: `session:path` — prefixes containing `/` or `\`, single
  letters (drive letters), and empty prefixes are always local. Empty path
  after the colon means the remote home (`.`).
- Forward specs follow ssh ordering: `-L local:remote`, `-R remote:local`.
- Go's flag package stops at the first positional, so flags must precede the
  remote command (same as ssh).

### Auth order (internal/ssh)

1. Explicit `--pswd` — password + keyboard-interactive, tried **first** so
   offering many agent keys cannot trip the server's MaxAuthTries limit.
2. Explicit `-i` key. Passphrase-protected keys produce a clear error
   pointing at ssh-agent (no interactive prompt by design).
3. With no explicit key: ssh-agent (`SSH_AUTH_SOCK`), then the default key
   files `~/.ssh/id_ed25519`, `id_ecdsa`, `id_rsa`, `id_dsa` (unreadable or
   passphrase-protected ones are skipped).

The agent connection is used only for the auth handshake and closed right
after `ssh.Dial` returns, so the long-lived daemon does not leak fds across
reconnects.

Client RPC deadlines: 10s default; 30s for connect/use (SSH dial budget);
`-t N` exec extends the deadline to N + 30s; unlimited `--stream` has no deadline.

## 14. Dependencies

```
golang.org/x/crypto/ssh            # SSH client
golang.org/x/crypto/ssh/knownhosts # host key verification
golang.org/x/sys/unix              # setsid, peer credentials
github.com/pkg/sftp                # SFTP client
github.com/google/uuid             # forward IDs
```

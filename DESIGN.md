# agmux - Architecture Design

agmux (Agent Multiplexer) is a session management and command execution tool for AI agents.
Inspired by tmux's client-server architecture, it manages both SSH and local sessions,
providing session switching with reconnect, structured output, and automatic reconnect.

## 1. Architecture Overview

```
┌──────────────┐  Unix Socket (0600)  ┌──────────────────┐
│  agmux CLI   │ ──────────────────── │  agmux-server    │
│  (thin client│  imsg binary protocol│  (daemon, persist)│
└──────────────┘                       │                  │
                                       │  Session Manager │
                                       │  ┌──────────────┤
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

## 2. Directory Structure

```
agmux/
├── cmd/
│   ├── agmux/main.go              # CLI client
│   └── agmux-server/main.go       # Server daemon
├── internal/
│   ├── protocol/
│   │   ├── msg.go                  # Message type constants
│   │   ├── types.go                # Request/Response struct definitions
│   │   ├── encode.go               # JSON payload encoding for imsg
│   │   └── encode_test.go
│   ├── server/
│   │   ├── server.go               # Server core: Unix socket listener, dispatch
│   │   ├── conn.go                 # Client connection handling
│   │   ├── server_test.go
│   ├── session/
│   │   ├── session.go              # NamedSession struct, lifecycle states
│   │   ├── ssh_session.go          # SSH session type
│   │   ├── local_session.go        # Local session type
│   │   ├── manager.go              # Session registry, named lookup, default
│   │   ├── manager_test.go
│   ├── ssh/
│   │   ├── client.go               # SSH client wrapper, auth, TOFU, Connect
│   │   ├── client_test.go
│   ├── exec/
│   │   ├── executor.go             # Command execution for both SSH and local
│   │   ├── local.go                # Local command execution
│   │   ├── remote.go               # Remote (SSH) command execution
│   │   ├── sudo.go                 # Sudo via stdin helper
│   │   ├── executor_test.go
│   ├── portforward/
│   │   ├── forwarder.go            # Local/remote port forwarder
│   │   ├── service.go              # Forward service (registry, lifecycle)
│   │   ├── forwarder_test.go
│   ├── transfer/
│   │   ├── sftp.go                 # SFTP upload/download/sync/directory ops
│   │   ├── sftp_test.go
│   ├── reconnect/
│   │   ├── monitor.go              # Health check + exponential backoff reconnect
│   │   ├── monitor_test.go
│   ├── persist/
│   │   ├── store.go                # State persistence: save/load session metadata
│   │   ├── store_test.go
│   ├── audit/
│   │   ├── log.go                  # Audit logging
│   │   ├── log_test.go
├── pkg/
│   ├── imsg/
│   │   ├── imsg.go                 # Imsg protocol: read/write binary frames
│   │   ├── imsg_test.go
├── go.mod
├── go.sum
├── Makefile
├── DESIGN.md
├── .gitignore
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
- **Type** (uint16): Message type constant
- **Length** (uint32): Payload length in bytes
- **Payload**: JSON-encoded request params or response result

### Message Types

```go
// Client → Server (requests)
const (
    MsgConnect       uint16 = 1   // Connect to SSH host
    MsgLocal         uint16 = 2   // Create local session
    MsgDetach        uint16 = 3   // Detach from session
    MsgKill          uint16 = 4   // Kill session
    MsgAttach        uint16 = 5   // Attach to existing session
    MsgExec          uint16 = 6   // Execute command (SSH or local)
    MsgLocalExec     uint16 = 7   // Execute command locally (no session needed)
    MsgList          uint16 = 8   // List sessions
    MsgUse           uint16 = 9   // Set default session
    MsgForward       uint16 = 10  // Start port forward
    MsgForwards      uint16 = 11  // List forwards
    MsgForwardClose  uint16 = 12  // Close forward
    MsgSCP           uint16 = 13  // File transfer
    MsgSFTPLs        uint16 = 14  // SFTP list directory
    MsgSFTPMkdir     uint16 = 15  // SFTP mkdir
    MsgSFTPRm        uint16 = 16  // SFTP remove
    MsgReconnect     uint16 = 17  // Removed — use MsgUse with -p/-i instead
    MsgPing          uint16 = 18  // Health check
)

// Server → Client (responses)
const (
    MsgResult  uint16 = 100  // Success result
    MsgError   uint16 = 101  // Error response
    MsgPong    uint16 = 102  // Ping response
)
```

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

ReadMessage reads the 7-byte header first, then reads exactly Length bytes of payload.
No newline-delimited ambiguity — explicit length field solves partial reads.

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
    wg         sync.WaitGroup
    shutdown   chan struct{}
}

func NewServer(opts ServerOptions) *Server
func (s *Server) Start() error              // Listen on Unix socket, 0600 permissions
func (s *Server) Stop() error               // Graceful shutdown: persist → kill all → wait
func (s *Server) handleConn(conn net.Conn)  // Per-client goroutine: read imsg → dispatch → write
func (s *Server) dispatch(msg *Imsg) (*Imsg, error) // Route by Type to service
```

### Graceful Shutdown Sequence

1. Close listener (reject new connections)
2. Persist session state to disk
3. Kill all SSH sessions (close connections)
4. Wait for goroutines to finish
5. Clean up socket file

### Signal Handling (cmd/agmux-server/main.go)

```go
sigCh := make(chan os.Signal, 1)
signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
<-sigCh
server.Stop()
```

## 5. Session Model

### Session Interface

Both SSH and local sessions share a common interface:

```go
type Session interface {
    GetName() string
    GetHost() string    // "local" for local sessions
    GetUser() string    // OS username for local sessions
    GetStatus() Status
    SetStatus(Status)
    Close() error
    IsLocal() bool
}
```

### SSH Session (internal/session/ssh_session.go)

```go
type SSHSession struct {
    Name      string
    Host      string
    User      string
    Port      int
    Password  string       // In-memory only, not persisted
    KeyPath   string
    Status    Status
    Client    *ssh.Client  // golang.org/x/crypto/ssh.Client
    CreatedAt time.Time
    LastCmd   string
    mu        sync.RWMutex
}
```

### Local Session (internal/session/local_session.go)

```go
type LocalSession struct {
    Name      string       // Auto-generated: "local-{hostname}"
    Host      string       // Always "local"
    User      string       // Current OS user
    Status    Status       // Always "connected" (no reconnect needed)
    CreatedAt time.Time
    LastCmd   string
    mu        sync.RWMutex
}
```

Local sessions have no reconnect, no port forwarding, no file transfer — just command execution.
They exist so agents can manage both remote and local operations under the same session framework.

### Status Lifecycle

```
connecting → connected → disconnected (after kill)
                       → reconnecting → connected / offline
                       → offline → (use -p/-i → connected)
```

- **connected**: SSH alive, session usable
- **disconnected**: SSH closed (after kill)
- **reconnecting**: connection lost, attempting reconnect
- **offline**: reconnect failed, agent must `use -p/-i` to restore

### Manager (internal/session/manager.go)

```go
type Manager struct {
    sessions map[string]Session  // keyed by Name
    default  string              // default session name
    mu       sync.RWMutex
}

func (m *Manager) ConnectSSH(name, user, host string, port int, password, keyPath string) (*SSHSession, error)
func (m *Manager) ConnectLocal(name string) (*LocalSession, error)
func (m *Manager) Detach(name string) error
func (m *Manager) Attach(name string) error    // For SSH: just set status, for Local: no-op
func (m *Manager) Kill(name string) error
func (m *Manager) List() []Session
func (m *Manager) Use(name string) error
func (m *Manager) Get(name string) (Session, error)
func (m *Manager) GetDefault() (Session, error)
func (m *Manager) KillAll()
```

### Naming Rules

- Name must match `[a-zA-Z0-9_-]`, 1-64 chars
- SSH session auto-name if not provided: `{user}@{host}` (e.g. `admin@10.0.1.1`)
- Local session auto-name: `local` (only one local session, auto-created on daemon start)
- Dedup: same user+host+port returns existing session

## 6. Command Execution

### Executor (internal/exec/executor.go)

Unified executor that dispatches to remote or local based on session type:

```go
type Executor struct {
    sessions *session.Manager
    audit    *audit.Logger
}

func (e *Executor) Exec(sessionName, command string, timeout int, sudoOpts *SudoOptions) (*ExecResult, error)
```

### Remote Execution (internal/exec/remote.go)

```go
func execRemote(client *ssh.Client, command string, timeout int, sudoOpts *SudoOptions) (*ExecResult, error)
```

- Always shell-wrap: `fullCmd = /bin/sh -c <quoted command>`
- No PTY, no CombinedOutput — stdout/stderr via Pipe separation
- Timeout: goroutine + timer, SIGKILL on timeout

### Local Execution (internal/exec/local.go)

```go
func execLocal(command string, timeout int, sudoOpts *SudoOptions) (*ExecResult, error)
```

- Uses `os/exec.Cmd` with StdoutPipe/StderrPipe
- Always shell-wrap: `/bin/sh -c <quoted command>`
- Same timeout mechanism as remote

### Sudo (internal/exec/sudo.go)

Safe sudo implementation — NO shell concatenation of passwords:

```go
type SudoOptions struct {
    Enabled  bool
    Password string
    User     string   // -u user
    Login    bool     // -i login shell
}

// Implementation: sudo -S via stdin
// 1. Build sudo command with -S -p ''
// 2. Start command
// 3. Write password + "\n" to stdin
// 4. Close stdin and wait for completion

func writePassword(stdin io.WriteCloser, password string) error
func buildSudoCommand(sudoOpts *SudoOptions, command string) string
```

### ExecResult

```go
type ExecResult struct {
    Stdout   string `json:"stdout"`
    Stderr   string `json:"stderr"`
    ExitCode int    `json:"exit_code"`
}
```

### Quick Local Exec (MsgLocalExec)

For one-off local commands without needing a session:

```go
// Direct local exec — no session required
func (e *Executor) ExecLocal(command string, timeout int, sudoOpts *SudoOptions) (*ExecResult, error)
```

## 7. Port Forwarding

### Service (internal/portforward/service.go)

```go
type Service struct {
    sessions *session.Manager
    forwards map[string]*Forwarder   // keyed by UUID (full, not truncated)
    mu       sync.RWMutex
}

func (s *Service) Add(sessionName, forwardType string, localPort, remotePort int) (*ForwardInfo, error)
func (s *Service) List() []*ForwardInfo
func (s *Service) Close(forwardID string) error
func (s *Service) RestartAll(sessionName string) error   // After reconnect
```

### Forwarder (internal/portforward/forwarder.go)

Same design as gssh but with improved lifecycle:

```go
type Forwarder struct {
    ID         string
    Session    string           // session name
    Type       string           // "local" | "remote"
    LocalPort  int
    RemotePort int
    sshClient  *ssh.Client
    listener   net.Listener
    conns      map[net.Conn]bool
    closed     bool
    wg         sync.WaitGroup
    lifecycleMu sync.Mutex
}
```

- **Local forward**: `localhost:localPort → SSH tunnel → remote:remotePort`
- **Remote forward**: `remote:remotePort → SSH tunnel → localhost:localPort`
- Both use bidirectional `io.Copy`
- `Restart(newSSHClient)` for reconnect recovery (stops old goroutines first)

## 8. File Transfer

### SFTP Service (internal/transfer/sftp.go)

```go
type Service struct {
    sessions *session.Manager
}

func (s *Service) Upload(sessionName, localPath, remotePath string) (*TransferResult, error)
func (s *Service) Download(sessionName, remotePath, localPath string) (*TransferResult, error)
func (s *Service) ListDir(sessionName, path string) ([]string, error)
func (s *Service) Mkdir(sessionName, path string) error
func (s *Service) Remove(sessionName, path string) error
```

Only available for SSH sessions. Local sessions return error "not available for local session".

- Incremental sync: skip files with same size+mtime
- Path traversal check: `filepath.IsLocal()` for downloads
- Permission sync: preserve file mode on both sides

## 9. Reconnect Monitor

### Monitor (internal/reconnect/monitor.go)

```go
type Monitor struct {
    sessions *session.Manager
    forwards *portforward.Service
}

func (m *Monitor) Watch(s *SSHSession)   // Start watching an SSH session
```

**Exponential backoff**: 5s → 10s → 20s → 40s → max 5min
- Success resets backoff to 5s
- Health check: `ssh.Client.SendRequest("keepalive@agmux", true, nil)` with 5s timeout
- After reconnect success: call `forwards.RestartAll(sessionName)` to restore forwards

## 10. State Persistence

### Store (internal/persist/store.go)

```go
type Store struct {
    path string   // ~/.sshmux/state.json  (TODO: rename to ~/.agmux/state.json)
}

type SessionState struct {
    Name      string    `json:"name"`
    Type      string    `json:"type"`     // "ssh" or "local"
    Host      string    `json:"host"`
    User      string    `json:"user"`
    Port      int       `json:"port,omitempty"`
    KeyPath   string    `json:"key_path,omitempty"`
    Status    string    `json:"status"`
    CreatedAt time.Time `json:"created_at"`
    // Password NOT persisted
}

func (s *Store) Save(sessions []*SessionState) error
func (s *Store) Load() ([]*SessionState, error)
```

On daemon startup:
- Load state from disk
- Auto-create local session
- SSH sessions: attempt reconnect (status `connected`)
  - If no password/key available → status `offline`, agent must `use -p password`
  - If key available → auto-reconnect

## 11. Audit Logging

### Logger (internal/audit/log.go)

```go
type Logger struct {
    file *os.File    // ~/.agmux/audit.log
}

type Entry struct {
    Timestamp time.Time `json:"timestamp"`
    Session   string    `json:"session"`
    Action    string    `json:"action"`    // connect, use, kill, exec, forward, etc.
    Command   string    `json:"cmd,omitempty"`
    Result    string    `json:"result"`    // success, error, timeout
    Detail    string    `json:"detail,omitempty"`
}

func (l *Logger) Log(entry Entry) error   // Append one JSON line
```

## 12. Security

1. **Socket auth**: Unix socket with `0600` permissions. Only the OS user who started the daemon can connect. Same model as tmux.
2. **Sudo safety**: Password via `sudo -S` stdin write. Never shell-concatenated.
3. **Password handling**: Not persisted to disk. In memory only during session lifetime.
4. **Path traversal**: `filepath.IsLocal()` check for SFTP downloads.
5. **Audit log**: Every action logged to `~/.agmux/audit.log`.

## 13. CLI Commands

```
agmux start                                  # Start daemon (fork to background)
agmux connect -u user -h host [-P port] [-n name] [-p password] [-i key]
agmux local [-n name]                        # Create local session
agmux kill [-n name]                         # Kill session (close SSH)
agmux exec [-n name] [-t timeout] [--sudo ...] "command"
agmux run [-t timeout] [--sudo ...] "command" # One-off local exec (no session)
agmux list | ls                              # List sessions
agmux use <name> [-p password] [-i key]      # Switch default / reconnect session
agmux forward [-n name] -l local -r remote [-R] [--bind addr|--public]
agmux forwards                               # List forwards
agmux forward-close <id>
agmux scp [-n name] -put|-get <src> <dst>
agmux sync [-n name] -put|-get <src> <dst>
agmux sftp [-n name] -c ls|mkdir|rm -d <path>
agmux ping                                   # Check daemon alive
agmux stop                                   # Stop daemon gracefully
agmux -v, --version
agmux -S <socket_path>                       # Override socket path
```

Global flags:
- `-S socket_path`: Override socket path (default: `$XDG_RUNTIME_DIR/agmux/agmux.sock`, fallback `~/.agmux/run/agmux.sock`)
- `-v, --version`: Show version

## 14. Implementation Order

1. pkg/imsg (protocol foundation)
2. internal/protocol (message types)
3. internal/ssh (SSH client)
4. internal/session (session model + manager)
5. internal/exec (command execution, both remote and local)
6. internal/server (server core + dispatch)
7. cmd/agmux-server (daemon entry point)
8. cmd/agmux (CLI client)
9. internal/portforward
10. internal/transfer
11. internal/reconnect
12. internal/persist
13. internal/audit
14. Tests

## 15. Dependencies

```
golang.org/x/crypto/ssh            # SSH client library
golang.org/x/crypto/ssh/knownhosts # TOFU host key
github.com/pkg/sftp                # SFTP client
github.com/google/uuid              # UUID for forward IDs
```

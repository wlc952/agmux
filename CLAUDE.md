# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Project

gssh is a tmux-style client-server tool that lets AI agents run commands on remote (SSH) and local sessions through a persistent daemon. Core design constraint: **fully non-interactive, scriptable, no TTY anywhere** — all parameters come from CLI flags.

Single binary: `cmd/gssh` is both the thin CLI client and the daemon (`gssh server` subcommand). The CLI auto-starts the daemon (detached, setsid, logs to `~/.gssh/server.log`) whenever a command needs it; only `start`/`stop`/`ping`/`server` skip auto-start (via the `allowAutoStart` flag in main.go).

## Commands

```bash
make build      # builds bin/gssh
make test       # go test -cover ./... — all tests are hermetic unit tests (no SSH server needed)
make install    # copies binary to /usr/local/bin
go vet ./...    # no other linter configured; keep code gofmt-clean
```

Run a single test: `go test ./internal/session/ -run TestName -v`

Tests must stay hermetic: use `t.TempDir()` + `t.Setenv("HOME", ...)` so they never touch the real `~/.gssh` or `~/.ssh` (see `internal/server/server_test.go` for the pattern).

## Architecture

Client and daemon communicate over a Unix socket (0600) using `imsg`, a binary frame protocol (`pkg/imsg`): 7-byte header (1B version, 2B type, 4B big-endian length) + JSON payload, 4 MB max.

**Request flow** — adding a new command touches four places:
1. `internal/protocol/msg.go` — message type constant (removed types 3, 5, 17 are deliberately vacant; do not reuse)
2. `internal/protocol/types.go` — params/result struct
3. `internal/server/server.go` — `dispatch()` switch case (the daemon's only router)
4. `cmd/gssh/commands.go` — CLI handler calling `sendRequest()`

**Streaming exec is the exception**: `MsgExecStream`/`MsgLocalExecStream` bypass the single-response dispatch loop in `handleConn` — the handler writes multiple `MsgStreamChunk` frames directly to the conn, terminated by one `MsgStreamEnd` carrying the exit code. Responses over the 4 MB frame cap are rejected with a "use --stream" error, never truncated mid-write.

**Package layout:**
- `cmd/gssh/` — `main.go` (dispatch/usage), `client.go` (dial + auto-start + RPC), `server.go` (daemon subcommand), `commands.go` (handlers), `spawn_*.go` (setsid), `peercred_*.go` (UID check)
- `internal/session/session.go` — single file holds the `Session` interface, `SSHSession`, `LocalSession`, and `Manager` (registry + default session). Status lifecycle: `connecting → connected → reconnecting → offline | disconnected`. `Reconnect()` restores offline sessions without stealing the default (used by state restore).
- `internal/exec/executor.go` — single file holds the full execution matrix: remote/local × buffered/stream × normal/sudo. Commands are wrapped `/bin/bash -c <shellquote.Quote(cmd)>` (never Go `%q` — it corrupts newlines). Timeout sends SIGKILL and returns exit code **-1**. A non-zero exit status is *not* an error — it's returned as `ExitCode`.
- `internal/shellquote/` — POSIX single-quote escaping shared by CLI and executor (also guards `--sudo-user` against injection).
- `internal/ssh/client.go` — SSH dial + auth + host-key policy. TOFU auto-accept of unknown host keys is **off by default**; only via `GSSH_INSECURE_ACCEPT_NEW_HOST_KEYS=1`.
- `internal/reconnect/monitor.go` — 5s health checks (`keepalive@gssh` SSH request), exponential backoff 5s→2×→max 5min; on reconnect success calls `forwards.RestartAll`.
- `internal/portforward/forwarder.go` — Forwarder + Service; `Close` accepts full UUID or unambiguous prefix (CLI shows 8-char prefixes).
- `internal/persist/store.go` — daemon `Stop()` saves session metadata to `~/.gssh/state.json` (passwords are **never** persisted); on `Start()`, `restoreState()` re-registers SSH sessions as `offline` and auto-reconnects key-based ones in the background.
- `internal/audit/log.go` — every action appended as one JSON line to `~/.gssh/audit.log`.
- `internal/socketpath/` — socket path resolution (`$XDG_RUNTIME_DIR/gssh/gssh.sock`, fallback `~/.gssh/run/gssh.sock`) + ownership/permission validation.

## Security model (layered, keep all layers when modifying)

1. Socket file is 0600; `socketpath.Validate` rejects non-socket, group/world-accessible, or foreign-owned paths.
2. CLI verifies daemon peer credentials via platform-specific `peercred_{darwin,linux,other}.go` (build-tagged).
3. Sudo passwords go through `sudo -S` stdin writes only — never concatenate passwords into shell strings.
4. SFTP downloads check `filepath.IsLocal()` against path traversal.

## Conventions

- Flags: `-p` = password, `-P` = port (except `sftp`, where `-p` is the remote path).
- Client RPC deadlines: 10s default, 30s for connect/use (SSH dial budget), `-t N` + 30s for exec, none for unlimited `--stream`.
- Graceful shutdown order in `Server.Stop()`: close listener → persist state → kill sessions → drain conns → remove socket. `Stop()` must stay idempotent (tested). `runDaemon` exits when `Start()` returns — RPC-triggered stops must not leak the process.
- Releases: pushing any tag triggers `.github/workflows/release.yml` cross-compilation of the single binary (Windows included; daemon features need Unix sockets).
- README.md is written in Chinese; DESIGN.md in English. Match the language of the file you're editing.

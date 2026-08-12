package server

import (
	"encoding/json"
	"fmt"
	"log"
	"net"
	"os"
	"sync"
	"time"

	"gssh/internal/audit"
	"gssh/internal/exec"
	"gssh/internal/persist"
	"gssh/internal/portforward"
	"gssh/internal/protocol"
	"gssh/internal/reconnect"
	"gssh/internal/session"
	"gssh/internal/socketpath"
	"gssh/internal/transfer"

	"gssh/pkg/imsg"
)

// Server is the core daemon that listens on a Unix socket and dispatches commands.
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
	stopOnce   sync.Once
	connMu     sync.Mutex
	activeConn map[net.Conn]struct{}
	acceptMu   sync.Mutex
	stopping   bool
}

// NewServer creates a new server instance.
func NewServer(socketPath string) *Server {
	sessions := session.NewManager()

	s := &Server{
		socketPath: socketPath,
		sessions:   sessions,
		executor:   exec.NewExecutor(sessions),
		forwards:   portforward.NewService(sessions),
		transfer:   transfer.NewService(sessions),
		persist:    persist.NewStore(),
		audit:      audit.NewLogger(),
		shutdown:   make(chan struct{}),
		activeConn: make(map[net.Conn]struct{}),
	}

	// Reconnect monitor uses the actual forwards service.
	s.reconnect = reconnect.NewMonitor(sessions, s.forwards)

	return s
}

// Start begins listening on the Unix socket.
func (s *Server) Start() error {
	if err := socketpath.EnsureParentDir(s.socketPath); err != nil {
		return fmt.Errorf("failed to create socket parent directory: %w", err)
	}
	if err := socketpath.RemoveIfOwnedSocket(s.socketPath); err != nil {
		return err
	}

	listener, err := net.Listen("unix", s.socketPath)
	if err != nil {
		return fmt.Errorf("failed to listen on %s: %w", s.socketPath, err)
	}

	// Set socket permissions to 0600 (owner only)
	if err := os.Chmod(s.socketPath, 0600); err != nil {
		listener.Close()
		return fmt.Errorf("failed to set socket permissions: %w", err)
	}

	s.listener = listener
	log.Printf("[server] Listening on %s", s.socketPath)

	// Restore previous state if available
	s.restoreState()

	for {
		conn, err := listener.Accept()
		if err != nil {
			select {
			case <-s.shutdown:
				return nil // graceful shutdown
			default:
				log.Printf("[server] Accept error: %v", err)
				continue
			}
		}
		select {
		case <-s.shutdown:
			conn.Close()
			return nil
		default:
		}
		s.acceptMu.Lock()
		if s.stopping {
			s.acceptMu.Unlock()
			conn.Close()
			return nil
		}
		s.addConn(conn)
		s.wg.Add(1)
		s.acceptMu.Unlock()
		go s.handleConn(conn)
	}
}

// Stop performs graceful shutdown.
func (s *Server) Stop() error {
	s.stopOnce.Do(func() {
		log.Println("[server] Shutting down...")

		close(s.shutdown)
		s.acceptMu.Lock()
		s.stopping = true
		s.acceptMu.Unlock()

		// 1. Close listener
		if s.listener != nil {
			s.listener.Close()
		}

		// 2. Persist state
		states := persist.CollectState(s.sessions.List())
		if err := s.persist.Save(states); err != nil {
			log.Printf("[server] Failed to persist state: %v", err)
		}

		// 3. Kill all sessions
		s.sessions.KillAll()

		// 4. Drain active handlers. Connections can appear while shutdown is in progress,
		// so keep closing tracked conns until all handlers exit.
		waitDone := make(chan struct{})
		go func() {
			s.wg.Wait()
			close(waitDone)
		}()
	drainLoop:
		for {
			s.closeActiveConns()
			select {
			case <-waitDone:
				break drainLoop
			case <-time.After(50 * time.Millisecond):
			}
		}

		// 5. Cleanup
		if err := socketpath.RemoveIfOwnedSocket(s.socketPath); err != nil && !os.IsNotExist(err) {
			log.Printf("[server] Failed to remove socket: %v", err)
		}
		s.audit.Close()

		log.Println("[server] Shutdown complete")
	})

	return nil
}

func (s *Server) handleConn(conn net.Conn) {
	defer s.wg.Done()
	defer func() {
		s.removeConn(conn)
		conn.Close()
	}()

	for {
		msg, err := imsg.ReadMessage(conn)
		if err != nil {
			return // connection closed or error
		}

		// Streaming exec: the handler writes multiple frames directly to conn
		// before returning, so it bypasses the single-response dispatch loop.
		if msg.Type == protocol.MsgExecStream {
			var params protocol.ExecParams
			if err := protocol.DecodePayload(msg.Payload, &params); err != nil {
				errPayload, _ := json.Marshal(protocol.ErrorPayload{Code: -32000, Message: err.Error()})
				imsg.WriteMessage(conn, imsg.NewImsg(protocol.MsgError, errPayload))
			} else {
				s.handleExecStream(conn, params)
			}
			continue
		}

		if msg.Type == protocol.MsgLocalExecStream {
			var params protocol.LocalExecParams
			if err := protocol.DecodePayload(msg.Payload, &params); err != nil {
				errPayload, _ := json.Marshal(protocol.ErrorPayload{Code: -32000, Message: err.Error()})
				imsg.WriteMessage(conn, imsg.NewImsg(protocol.MsgError, errPayload))
			} else {
				s.handleLocalExecStream(conn, params)
			}
			continue
		}

		response, err := s.dispatch(msg)
		if err != nil {
			// Send error response
			errPayload, _ := json.Marshal(protocol.ErrorPayload{Code: -32000, Message: err.Error()})
			response = imsg.NewImsg(protocol.MsgError, errPayload)
		}

		// Guard against oversized payloads (e.g. huge buffered exec output):
		// fail with a clear error instead of dying mid-write or dropping the conn.
		if len(response.Payload) > imsg.MaxPayloadSize {
			errPayload, _ := json.Marshal(protocol.ErrorPayload{
				Code:    -32000,
				Message: fmt.Sprintf("response exceeds %d bytes; re-run with --stream for large output", imsg.MaxPayloadSize),
			})
			response = imsg.NewImsg(protocol.MsgError, errPayload)
		}

		if err := imsg.WriteMessage(conn, response); err != nil {
			return
		}
	}
}

func (s *Server) dispatch(msg *imsg.Imsg) (*imsg.Imsg, error) {
	switch msg.Type {
	case protocol.MsgPing:
		return imsg.NewImsg(protocol.MsgPong, []byte(`{"status":"ok"}`)), nil

	case protocol.MsgConnect:
		var params protocol.ConnectParams
		if err := protocol.DecodePayload(msg.Payload, &params); err != nil {
			return nil, fmt.Errorf("invalid params: %w", err)
		}
		sess, err := s.sessions.ConnectSSH(params.Name, params.User, params.Host, params.Port, params.Password, params.KeyPath)
		if err != nil {
			s.audit.Log(audit.Entry{Action: "connect", Result: "error", Detail: err.Error()})
			return nil, err
		}
		// Start reconnect monitor for SSH sessions
		s.reconnect.Watch(sess)
		s.audit.Log(audit.Entry{Session: sess.GetName(), Action: "connect", Result: "success"})
		info := sessionToInfo(sess)
		payload, _ := protocol.EncodePayload(info)
		return imsg.NewImsg(protocol.MsgResult, payload), nil

	case protocol.MsgLocal:
		var params protocol.LocalParams
		if err := protocol.DecodePayload(msg.Payload, &params); err != nil {
			return nil, fmt.Errorf("invalid params: %w", err)
		}
		sess, err := s.sessions.ConnectLocal(params.Name)
		if err != nil {
			return nil, err
		}
		info := sessionToInfo(sess)
		payload, _ := protocol.EncodePayload(info)
		return imsg.NewImsg(protocol.MsgResult, payload), nil

	case protocol.MsgKill:
		var params protocol.KillParams
		if err := protocol.DecodePayload(msg.Payload, &params); err != nil {
			return nil, fmt.Errorf("invalid params: %w", err)
		}
		s.reconnect.StopWatch(params.Name)
		err := s.sessions.Kill(params.Name)
		if err != nil {
			return nil, err
		}
		s.audit.Log(audit.Entry{Session: params.Name, Action: "kill", Result: "success"})
		return imsg.NewImsg(protocol.MsgResult, []byte(`{"status":"killed"}`)), nil

	case protocol.MsgExec:
		var params protocol.ExecParams
		if err := protocol.DecodePayload(msg.Payload, &params); err != nil {
			return nil, fmt.Errorf("invalid params: %w", err)
		}
		result, err := s.executor.Exec(params.Name, params.Command, params.Timeout, &params.Sudo)
		if err != nil {
			s.logExecAudit(params.Name, params.Command, err, nil)
			return nil, err
		}
		s.logExecAudit(params.Name, params.Command, nil, result)
		payload, _ := protocol.EncodePayload(result)
		return imsg.NewImsg(protocol.MsgResult, payload), nil

	case protocol.MsgLocalExec:
		var params protocol.LocalExecParams
		if err := protocol.DecodePayload(msg.Payload, &params); err != nil {
			return nil, fmt.Errorf("invalid params: %w", err)
		}
		result, err := s.executor.ExecLocal(params.Command, params.Timeout, &params.Sudo)
		if err != nil {
			s.logExecAudit("", params.Command, err, nil)
			return nil, err
		}
		s.logExecAudit("", params.Command, nil, result)
		payload, _ := protocol.EncodePayload(result)
		return imsg.NewImsg(protocol.MsgResult, payload), nil

	case protocol.MsgList:
		sessions := s.sessions.List()
		infos := make([]protocol.SessionInfo, len(sessions))
		for i, sess := range sessions {
			infos[i] = sessionToInfo(sess)
		}
		payload, _ := protocol.EncodePayload(infos)
		return imsg.NewImsg(protocol.MsgResult, payload), nil

	case protocol.MsgUse:
		var params protocol.UseParams
		if err := protocol.DecodePayload(msg.Payload, &params); err != nil {
			return nil, fmt.Errorf("invalid params: %w", err)
		}
		// Check status before Use to decide if reconnect monitor is needed
		sess, _ := s.sessions.Get(params.Name)
		wasOffline := false
		if sess != nil && !sess.IsLocal() {
			status := sess.(*session.SSHSession).GetStatus()
			wasOffline = status == session.StatusOffline || status == session.StatusDisconnected
		}

		err := s.sessions.Use(params.Name, params.Password, params.KeyPath)
		if err != nil {
			return nil, err
		}
		// Only register reconnect monitor if session was offline/disconnected before Use
		if wasOffline {
			sess, _ = s.sessions.Get(params.Name)
			if sess != nil && !sess.IsLocal() {
				s.reconnect.Watch(sess.(*session.SSHSession))
			}
		}
		return imsg.NewImsg(protocol.MsgResult, []byte(`{"status":"ok"}`)), nil

	case protocol.MsgForward:
		var params protocol.ForwardParams
		if err := protocol.DecodePayload(msg.Payload, &params); err != nil {
			return nil, fmt.Errorf("invalid params: %w", err)
		}
		info, err := s.forwards.Add(params.Name, params.Type, params.LocalPort, params.RemotePort, params.BindAddr)
		if err != nil {
			return nil, err
		}
		payload, _ := protocol.EncodePayload(info)
		return imsg.NewImsg(protocol.MsgResult, payload), nil

	case protocol.MsgForwards:
		forwards := s.forwards.List()
		payload, _ := protocol.EncodePayload(forwards)
		return imsg.NewImsg(protocol.MsgResult, payload), nil

	case protocol.MsgForwardClose:
		var params protocol.ForwardCloseParams
		if err := protocol.DecodePayload(msg.Payload, &params); err != nil {
			return nil, fmt.Errorf("invalid params: %w", err)
		}
		err := s.forwards.Close(params.ID)
		if err != nil {
			return nil, err
		}
		return imsg.NewImsg(protocol.MsgResult, []byte(`{"status":"closed"}`)), nil

	case protocol.MsgSCP:
		var params protocol.SCPParams
		if err := protocol.DecodePayload(msg.Payload, &params); err != nil {
			return nil, fmt.Errorf("invalid params: %w", err)
		}
		var result *protocol.TransferResult
		var err error
		if params.IsUpload {
			result, err = s.transfer.Upload(params.Name, params.Source, params.Dest)
		} else {
			result, err = s.transfer.Download(params.Name, params.Source, params.Dest)
		}
		if err != nil {
			s.logTransferAudit(params.Name, "scp", params.Source+" -> "+params.Dest, err, nil)
			return nil, err
		}
		s.logTransferAudit(params.Name, "scp", params.Source+" -> "+params.Dest, nil, result)
		payload, _ := protocol.EncodePayload(result)
		return imsg.NewImsg(protocol.MsgResult, payload), nil

	case protocol.MsgSFTPLs:
		var params protocol.SFTPParams
		if err := protocol.DecodePayload(msg.Payload, &params); err != nil {
			return nil, fmt.Errorf("invalid params: %w", err)
		}
		files, err := s.transfer.ListDir(params.Name, params.Path)
		if err != nil {
			s.audit.Log(audit.Entry{Session: params.Name, Action: "sftp_ls", Command: params.Path, Result: "error", Detail: err.Error()})
			return nil, err
		}
		s.audit.Log(audit.Entry{Session: params.Name, Action: "sftp_ls", Command: params.Path, Result: "success"})
		payload, _ := protocol.EncodePayload(files)
		return imsg.NewImsg(protocol.MsgResult, payload), nil

	case protocol.MsgSFTPMkdir:
		var params protocol.SFTPParams
		if err := protocol.DecodePayload(msg.Payload, &params); err != nil {
			return nil, fmt.Errorf("invalid params: %w", err)
		}
		err := s.transfer.Mkdir(params.Name, params.Path)
		if err != nil {
			s.audit.Log(audit.Entry{Session: params.Name, Action: "sftp_mkdir", Command: params.Path, Result: "error", Detail: err.Error()})
			return nil, err
		}
		s.audit.Log(audit.Entry{Session: params.Name, Action: "sftp_mkdir", Command: params.Path, Result: "success"})
		return imsg.NewImsg(protocol.MsgResult, []byte(`{"status":"created"}`)), nil

	case protocol.MsgSFTPRm:
		var params protocol.SFTPParams
		if err := protocol.DecodePayload(msg.Payload, &params); err != nil {
			return nil, fmt.Errorf("invalid params: %w", err)
		}
		err := s.transfer.Remove(params.Name, params.Path)
		if err != nil {
			s.audit.Log(audit.Entry{Session: params.Name, Action: "sftp_rm", Command: params.Path, Result: "error", Detail: err.Error()})
			return nil, err
		}
		s.audit.Log(audit.Entry{Session: params.Name, Action: "sftp_rm", Command: params.Path, Result: "success"})
		return imsg.NewImsg(protocol.MsgResult, []byte(`{"status":"removed"}`)), nil

	case protocol.MsgStop:
		// Trigger graceful shutdown from CLI
		go s.Stop()
		return imsg.NewImsg(protocol.MsgResult, []byte(`{"status":"stopping"}`)), nil

	default:
		return nil, fmt.Errorf("unknown message type: %d", msg.Type)
	}
}

func (s *Server) logExecAudit(sessionName, command string, err error, result *protocol.ExecResult) {
	if err != nil {
		s.audit.Log(audit.Entry{Session: sessionName, Action: "exec", Command: command, Result: "error", Detail: err.Error()})
		return
	}

	resultStr := "success"
	if result != nil && result.ExitCode != 0 {
		resultStr = fmt.Sprintf("exit_%d", result.ExitCode)
	}
	s.audit.Log(audit.Entry{Session: sessionName, Action: "exec", Command: command, Result: resultStr})
}

func (s *Server) logTransferAudit(sessionName, action, command string, err error, result *protocol.TransferResult) {
	if err != nil {
		s.audit.Log(audit.Entry{Session: sessionName, Action: action, Command: command, Result: "error", Detail: err.Error()})
		return
	}

	resultStr := "success"
	detail := ""
	if result != nil && !result.Success {
		resultStr = "error"
		detail = result.Message
	}
	s.audit.Log(audit.Entry{Session: sessionName, Action: action, Command: command, Result: resultStr, Detail: detail})
}

func sessionToInfo(sess session.Session) protocol.SessionInfo {
	port := 0
	if !sess.IsLocal() {
		sshSess := sess.(*session.SSHSession)
		port = sshSess.Port
	}

	return protocol.SessionInfo{
		Name:      sess.GetName(),
		Type:      sess.GetType(),
		Host:      sess.GetHost(),
		User:      sess.GetUser(),
		Port:      port,
		Status:    string(sess.GetStatus()),
		CreatedAt: sess.GetCreatedAt().Unix(),
		LastCmd:   sess.GetLastCmd(),
	}
}

func (s *Server) restoreState() {
	states, err := s.persist.Load()
	if err != nil {
		log.Printf("[server] Failed to load state: %v", err)
		return
	}

	if len(states) == 0 {
		return
	}

	log.Printf("[server] Restoring %d sessions from state", len(states))
	for _, state := range states {
		if state.Type == "local" {
			s.sessions.ConnectLocal(state.Name)
			continue
		}

		// SSH sessions: register as offline, then auto-reconnect in the
		// background when non-interactive auth is available (explicit key,
		// or default key material via agent/default key files). Password-only
		// sessions stay offline (passwords are never persisted) until the
		// agent runs `gssh use <name> --pswd password`.
		s.sessions.RegisterOfflineSession(
			state.Name, state.User, state.Host, state.Port,
			state.KeyPath, time.Unix(state.CreatedAt, 0),
		)
		// state.KeyPath check keeps pre-auto_reconnect state files working.
		if state.AutoReconnect || state.KeyPath != "" {
			go func(name string) {
				sess, err := s.sessions.Reconnect(name)
				if err != nil {
					log.Printf("[server] Auto-reconnect of restored session %s failed: %v", name, err)
					return
				}
				s.reconnect.Watch(sess)
				log.Printf("[server] Restored session %s via key auth", name)
			}(state.Name)
		}
	}
}

func (s *Server) addConn(conn net.Conn) {
	s.connMu.Lock()
	s.activeConn[conn] = struct{}{}
	s.connMu.Unlock()
}

func (s *Server) removeConn(conn net.Conn) {
	s.connMu.Lock()
	delete(s.activeConn, conn)
	s.connMu.Unlock()
}

func (s *Server) closeActiveConns() {
	s.connMu.Lock()
	conns := make([]net.Conn, 0, len(s.activeConn))
	for conn := range s.activeConn {
		conns = append(conns, conn)
	}
	s.connMu.Unlock()

	for _, conn := range conns {
		conn.Close()
	}
}

// chunkWriter is an io.Writer that sends each Write call as a MsgStreamChunk
// frame over the connection.  stdout and stderr share the same mutex so that
// their frames never interleave on the wire.
type chunkWriter struct {
	conn   net.Conn
	stream string
	mu     *sync.Mutex
}

func (w *chunkWriter) Write(p []byte) (int, error) {
	chunk := protocol.StreamChunk{
		Stream: w.stream,
		Data:   p,
	}
	payload, err := protocol.EncodePayload(chunk)
	if err != nil {
		return 0, err
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if err := imsg.WriteMessage(w.conn, imsg.NewImsg(protocol.MsgStreamChunk, payload)); err != nil {
		return 0, err
	}
	return len(p), nil
}

// sendStreamEnd writes a MsgStreamEnd frame to the connection.
func sendStreamEnd(conn net.Conn, exitCode int, execErr error) {
	end := protocol.StreamEnd{ExitCode: exitCode}
	if execErr != nil {
		end.Error = execErr.Error()
	}
	payload, _ := protocol.EncodePayload(end)
	imsg.WriteMessage(conn, imsg.NewImsg(protocol.MsgStreamEnd, payload))
}

func (s *Server) handleExecStream(conn net.Conn, params protocol.ExecParams) {
	mu := &sync.Mutex{}
	stdoutW := &chunkWriter{conn: conn, stream: "stdout", mu: mu}
	stderrW := &chunkWriter{conn: conn, stream: "stderr", mu: mu}

	exitCode, execErr := s.executor.ExecStream(params.Name, params.Command, params.Timeout, &params.Sudo, stdoutW, stderrW)
	s.logExecAudit(params.Name, params.Command, execErr, &protocol.ExecResult{ExitCode: exitCode})
	sendStreamEnd(conn, exitCode, execErr)
}

func (s *Server) handleLocalExecStream(conn net.Conn, params protocol.LocalExecParams) {
	mu := &sync.Mutex{}
	stdoutW := &chunkWriter{conn: conn, stream: "stdout", mu: mu}
	stderrW := &chunkWriter{conn: conn, stream: "stderr", mu: mu}

	exitCode, execErr := s.executor.ExecLocalStream(params.Command, params.Timeout, &params.Sudo, stdoutW, stderrW)
	s.logExecAudit("", params.Command, execErr, &protocol.ExecResult{ExitCode: exitCode})
	sendStreamEnd(conn, exitCode, execErr)
}

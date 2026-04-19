package server

import (
	"encoding/json"
	"fmt"
	"log"
	"net"
	"os"
	"sync"
	"time"

	"agmux/internal/audit"
	"agmux/internal/exec"
	"agmux/internal/persist"
	"agmux/internal/portforward"
	"agmux/internal/protocol"
	"agmux/internal/reconnect"
	"agmux/internal/session"
	"agmux/internal/transfer"

	"agmux/pkg/imsg"
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
	}

	// Reconnect monitor uses the actual forwards service.
	s.reconnect = reconnect.NewMonitor(sessions, s.forwards)

	return s
}

// Start begins listening on the Unix socket.
func (s *Server) Start() error {
	// Remove old socket file
	os.Remove(s.socketPath)

	listener, err := net.Listen("unix", s.socketPath)
	if err != nil {
		return fmt.Errorf("failed to listen on %s: %w", s.socketPath, err)
	}

	// Set socket permissions to 0600 (owner only)
	os.Chmod(s.socketPath, 0600)

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

		s.wg.Add(1)
		go s.handleConn(conn)
	}
}

// Stop performs graceful shutdown.
func (s *Server) Stop() error {
	s.stopOnce.Do(func() {
		log.Println("[server] Shutting down...")

		close(s.shutdown)

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

		// 4. Wait for goroutines
		s.wg.Wait()

		// 5. Cleanup
		os.Remove(s.socketPath)
		s.audit.Close()

		log.Println("[server] Shutdown complete")
	})

	return nil
}

func (s *Server) handleConn(conn net.Conn) {
	defer s.wg.Done()
	defer conn.Close()

	for {
		msg, err := imsg.ReadMessage(conn)
		if err != nil {
			return // connection closed or error
		}

		response, err := s.dispatch(msg)
		if err != nil {
			// Send error response
			errPayload, _ := json.Marshal(protocol.ErrorPayload{Code: -32000, Message: err.Error()})
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

	case protocol.MsgDetach:
		var params protocol.DetachParams
		if err := protocol.DecodePayload(msg.Payload, &params); err != nil {
			return nil, fmt.Errorf("invalid params: %w", err)
		}
		err := s.sessions.Detach(params.Name)
		if err != nil {
			return nil, err
		}
		s.audit.Log(audit.Entry{Session: params.Name, Action: "detach", Result: "success"})
		return imsg.NewImsg(protocol.MsgResult, []byte(`{"status":"detached"}`)), nil

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

	case protocol.MsgAttach:
		var params protocol.AttachParams
		if err := protocol.DecodePayload(msg.Payload, &params); err != nil {
			return nil, fmt.Errorf("invalid params: %w", err)
		}
		err := s.sessions.Attach(params.Name, params.Password, params.KeyPath)
		if err != nil {
			return nil, err
		}
		s.audit.Log(audit.Entry{Session: params.Name, Action: "attach", Result: "success"})
		sess, _ := s.sessions.Get(params.Name)
		if sess != nil && !sess.IsLocal() {
			sshSess := sess.(*session.SSHSession)
			s.reconnect.Watch(sshSess)
		}
		return imsg.NewImsg(protocol.MsgResult, []byte(`{"status":"attached"}`)), nil

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
		err := s.sessions.Use(params.Name)
		if err != nil {
			return nil, err
		}
		return imsg.NewImsg(protocol.MsgResult, []byte(`{"status":"ok"}`)), nil

	case protocol.MsgForward:
		var params protocol.ForwardParams
		if err := protocol.DecodePayload(msg.Payload, &params); err != nil {
			return nil, fmt.Errorf("invalid params: %w", err)
		}
		info, err := s.forwards.Add(params.Name, params.Type, params.LocalPort, params.RemotePort)
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

	case protocol.MsgReconnect:
		var params protocol.ReconnectParams
		if err := protocol.DecodePayload(msg.Payload, &params); err != nil {
			return nil, fmt.Errorf("invalid params: %w", err)
		}
		sess, err := s.sessions.Get(params.Name)
		if err != nil {
			return nil, err
		}
		if sess.IsLocal() {
			return nil, fmt.Errorf("local sessions cannot reconnect")
		}
		sshSess := sess.(*session.SSHSession)
		err = s.sessions.Attach(params.Name, sshSess.GetPassword(), sshSess.GetKeyPath())
		if err != nil {
			return nil, err
		}
		return imsg.NewImsg(protocol.MsgResult, []byte(`{"status":"reconnected"}`)), nil

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

		// SSH sessions: register as offline (agent must re-attach with credentials)
		s.sessions.RegisterOfflineSession(
			state.Name, state.User, state.Host, state.Port,
			state.KeyPath, time.Unix(state.CreatedAt, 0),
		)
	}
}

package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"time"

	"gssh/internal/protocol"
	"gssh/internal/socketpath"
	"gssh/pkg/imsg"
)

const rpcTimeout = 10 * time.Second

// connectRPCTimeout covers SSH dial (up to 10s) plus auth round-trips for
// commands that may establish a connection (connect, use-with-reconnect).
const connectRPCTimeout = 30 * time.Second

// socketDeadlineBuffer is the extra time added on top of the user-specified
// command timeout to give the server room to finish and respond before the
// client socket deadline fires.
const socketDeadlineBuffer = 30 * time.Second

// daemonReadyTimeout is how long to wait for a freshly spawned daemon to
// accept connections.
const daemonReadyTimeout = 5 * time.Second

// dialOnce attempts one connection to the daemon, validating the socket and
// verifying peer credentials.
func dialOnce() (net.Conn, error) {
	if err := socketpath.Validate(socketPath); err != nil {
		return nil, err
	}
	conn, err := net.DialTimeout("unix", socketPath, rpcTimeout)
	if err != nil {
		return nil, err
	}
	if err := verifyDaemonPeer(conn); err != nil {
		conn.Close()
		return nil, err
	}
	return conn, nil
}

// isRecoverableDialError reports whether the daemon is simply not running
// (missing or stale socket), as opposed to a security/permissions failure.
func isRecoverableDialError(err error) bool {
	return errors.Is(err, os.ErrNotExist) || errors.Is(err, syscall.ECONNREFUSED)
}

// dialDaemon connects to the daemon, transparently starting it when allowed
// and the socket is missing or stale.
func dialDaemon() (net.Conn, error) {
	conn, err := dialOnce()
	if err == nil {
		return conn, nil
	}
	if !allowAutoStart {
		return nil, fmt.Errorf("failed to connect to daemon (is it running?): %w", err)
	}
	if !isRecoverableDialError(err) {
		return nil, err
	}

	fmt.Fprintln(os.Stderr, "Starting gssh daemon...")
	if serr := startDaemon(); serr != nil {
		return nil, fmt.Errorf("failed to start daemon: %w", serr)
	}
	return waitForDaemon()
}

// startDaemon spawns `gssh server` as a detached process with output
// redirected to the daemon log file.
func startDaemon() error {
	self, err := os.Executable()
	if err != nil {
		return err
	}

	logPath := daemonLogPath()
	if err := os.MkdirAll(filepath.Dir(logPath), 0700); err != nil {
		return err
	}
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	defer logFile.Close()

	devNull, err := os.OpenFile(os.DevNull, os.O_RDONLY, 0)
	if err != nil {
		return err
	}
	defer devNull.Close()

	cmd := exec.Command(self, "server", "-S", socketPath)
	cmd.Stdin = devNull
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	setDetachAttrs(cmd)
	return cmd.Start()
}

// waitForDaemon polls until the daemon accepts connections.
func waitForDaemon() (net.Conn, error) {
	deadline := time.Now().Add(daemonReadyTimeout)
	var lastErr error
	for {
		conn, err := dialOnce()
		if err == nil {
			return conn, nil
		}
		lastErr = err
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("daemon did not become ready (see %s): %w", daemonLogPath(), lastErr)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func daemonLogPath() string {
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		return filepath.Join(home, ".gssh", "server.log")
	}
	return filepath.Join(os.TempDir(), fmt.Sprintf("gssh-%d-server.log", os.Getuid()))
}

// sendRequest sends an RPC request and waits for a response using the default
// rpcTimeout for both connection and response.
func sendRequest(method uint16, params interface{}) ([]byte, error) {
	return sendRequestWithTimeout(method, params, rpcTimeout)
}

// sendRequestWithCmdTimeout is like sendRequest but extends the socket deadline
// to accommodate a user-specified command timeout (e.g. from -t flag).
// cmdTimeoutSecs == 0 means no command timeout → use rpcTimeout.
func sendRequestWithCmdTimeout(method uint16, params interface{}, cmdTimeoutSecs int) ([]byte, error) {
	deadline := rpcTimeout
	if cmdTimeoutSecs > 0 {
		deadline = time.Duration(cmdTimeoutSecs)*time.Second + socketDeadlineBuffer
	}
	return sendRequestWithTimeout(method, params, deadline)
}

func sendRequestWithTimeout(method uint16, params interface{}, deadline time.Duration) ([]byte, error) {
	conn, err := dialDaemon()
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	if err := conn.SetDeadline(time.Now().Add(deadline)); err != nil {
		return nil, fmt.Errorf("failed to set RPC deadline: %w", err)
	}

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

// sendStreamRequest sends a streaming exec request and prints output chunks to
// stdout/stderr as they arrive, blocking until the server sends MsgStreamEnd.
// When cmdTimeoutSecs > 0 the socket deadline is set to timeout + buffer;
// when cmdTimeoutSecs == 0 (unlimited) no deadline is imposed.
func sendStreamRequest(method uint16, params interface{}, cmdTimeoutSecs int) error {
	conn, err := dialDaemon()
	if err != nil {
		return err
	}
	defer conn.Close()
	if cmdTimeoutSecs > 0 {
		deadline := time.Duration(cmdTimeoutSecs)*time.Second + socketDeadlineBuffer
		if err := conn.SetDeadline(time.Now().Add(deadline)); err != nil {
			return fmt.Errorf("failed to set deadline: %w", err)
		}
	}
	// When cmdTimeoutSecs == 0, no deadline: the connection waits as long as needed.

	payload, err := protocol.EncodePayload(params)
	if err != nil {
		return fmt.Errorf("failed to encode params: %w", err)
	}

	if err := imsg.WriteMessage(conn, imsg.NewImsg(method, payload)); err != nil {
		return fmt.Errorf("failed to send request: %w", err)
	}

	for {
		msg, err := imsg.ReadMessage(conn)
		if err != nil {
			return fmt.Errorf("failed to read stream: %w", err)
		}

		switch msg.Type {
		case protocol.MsgStreamChunk:
			var chunk protocol.StreamChunk
			if err := json.Unmarshal(msg.Payload, &chunk); err != nil {
				return fmt.Errorf("failed to decode chunk: %w", err)
			}
			if chunk.Stream == "stderr" {
				os.Stderr.Write(chunk.Data)
			} else {
				os.Stdout.Write(chunk.Data)
			}

		case protocol.MsgStreamEnd:
			var end protocol.StreamEnd
			if err := json.Unmarshal(msg.Payload, &end); err != nil {
				return fmt.Errorf("failed to decode stream end: %w", err)
			}
			if end.Error != "" {
				return fmt.Errorf("%s", end.Error)
			}
			if end.ExitCode != 0 {
				os.Exit(end.ExitCode)
			}
			return nil

		case protocol.MsgError:
			var errPayload protocol.ErrorPayload
			if err := json.Unmarshal(msg.Payload, &errPayload); err != nil {
				return fmt.Errorf("RPC error (unparseable)")
			}
			return fmt.Errorf("RPC error: %s", errPayload.Message)

		default:
			return fmt.Errorf("unexpected message type during stream: %d", msg.Type)
		}
	}
}

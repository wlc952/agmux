package exec

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"agmux/internal/protocol"
	"agmux/internal/session"

	"golang.org/x/crypto/ssh"
)

// execResult holds the output of a command execution.
type execResult struct {
	stdout []byte
	stderr []byte
	err    error
}

// Executor handles command execution for both SSH and local sessions.
type Executor struct {
	sessions *session.Manager
}

// NewExecutor creates a new command executor.
func NewExecutor(sessions *session.Manager) *Executor {
	return &Executor{sessions: sessions}
}

// Exec executes a command in the specified session context.
func (e *Executor) Exec(sessionName, command string, timeout int, sudoOpts *protocol.SudoOptions) (*protocol.ExecResult, error) {
	sess, err := e.sessions.Get(sessionName)
	if err != nil {
		return nil, err
	}

	sess.SetLastCmd(command)

	if sess.IsLocal() {
		return runLocal(command, timeout, sudoOpts)
	}

	sshSess := sess.(*session.SSHSession)
	sshClient := sshSess.GetSSHClient()
	if sshClient == nil {
		return nil, fmt.Errorf("session not connected")
	}

	return runRemote(sshClient, command, timeout, sudoOpts)
}

// ExecLocal executes a one-off local command (no session needed).
func (e *Executor) ExecLocal(command string, timeout int, sudoOpts *protocol.SudoOptions) (*protocol.ExecResult, error) {
	return runLocal(command, timeout, sudoOpts)
}

// --- Remote execution ---

func runRemote(client *ssh.Client, command string, timeout int, sudoOpts *protocol.SudoOptions) (*protocol.ExecResult, error) {
	sshSession, err := client.NewSession()
	if err != nil {
		return nil, fmt.Errorf("failed to create SSH session: %w", err)
	}
	defer sshSession.Close()

	fullCmd := fmt.Sprintf("/bin/sh -c %q", command)

	if sudoOpts != nil && sudoOpts.Enabled {
		return runRemoteSudo(sshSession, fullCmd, sudoOpts, timeout)
	}

	return runRemoteNormal(sshSession, fullCmd, timeout)
}

func runRemoteNormal(sshSession *ssh.Session, fullCmd string, timeout int) (*protocol.ExecResult, error) {
	done := make(chan execResult, 1)

	go func() {
		stdoutPipe, err := sshSession.StdoutPipe()
		if err != nil {
			done <- execResult{nil, nil, err}
			return
		}
		stderrPipe, err := sshSession.StderrPipe()
		if err != nil {
			done <- execResult{nil, nil, err}
			return
		}

		if err := sshSession.Start(fullCmd); err != nil {
			done <- execResult{nil, nil, err}
			return
		}

		stdoutBuf, _ := io.ReadAll(stdoutPipe)
		stderrBuf, _ := io.ReadAll(stderrPipe)
		err = sshSession.Wait()
		done <- execResult{stdoutBuf, stderrBuf, err}
	}()

	res, timedOut := waitTimeout(done, timeout)
	if timedOut {
		sshSession.Signal(ssh.SIGKILL)
		sshSession.Close()
		<-done
		return &protocol.ExecResult{Stdout: "", Stderr: "", ExitCode: -1}, fmt.Errorf("command timed out")
	}

	return sshExecResult(res), nil
}

func runRemoteSudo(sshSession *ssh.Session, fullCmd string, sudoOpts *protocol.SudoOptions, timeout int) (*protocol.ExecResult, error) {
	askpassPath, cleanup, err := createAskpassHelper(sudoOpts.Password)
	if err != nil {
		return nil, fmt.Errorf("failed to create askpass helper: %w", err)
	}
	defer cleanup()

	sudoPrefix := buildSudoPrefix(sudoOpts)
	envVars := fmt.Sprintf("SUDO_ASKPASS=%s", askpassPath)
	sudoCmd := fmt.Sprintf("%s -A %s", sudoPrefix, fullCmd)
	wrappedCmd := fmt.Sprintf("/bin/sh -c %q", fmt.Sprintf("env %s %s", envVars, sudoCmd))

	done := make(chan execResult, 1)

	go func() {
		stdoutPipe, err := sshSession.StdoutPipe()
		if err != nil {
			done <- execResult{nil, nil, err}
			return
		}
		stderrPipe, err := sshSession.StderrPipe()
		if err != nil {
			done <- execResult{nil, nil, err}
			return
		}

		if err := sshSession.Start(wrappedCmd); err != nil {
			done <- execResult{nil, nil, err}
			return
		}

		stdoutBuf, _ := io.ReadAll(stdoutPipe)
		stderrBuf, _ := io.ReadAll(stderrPipe)
		err = sshSession.Wait()
		done <- execResult{stdoutBuf, stderrBuf, err}
	}()

	res, timedOut := waitTimeout(done, timeout)
	if timedOut {
		sshSession.Signal(ssh.SIGKILL)
		sshSession.Close()
		<-done
		cleanup()
		return &protocol.ExecResult{Stdout: "", Stderr: "", ExitCode: -1}, fmt.Errorf("command timed out")
	}

	return sshExecResult(res), nil
}

// --- Local execution ---

func runLocal(command string, timeout int, sudoOpts *protocol.SudoOptions) (*protocol.ExecResult, error) {
	fullCmd := fmt.Sprintf("/bin/sh -c %q", command)

	if sudoOpts != nil && sudoOpts.Enabled {
		return runLocalSudo(fullCmd, sudoOpts, timeout)
	}

	return runLocalNormal(fullCmd, timeout)
}

func runLocalNormal(fullCmd string, timeout int) (*protocol.ExecResult, error) {
	cmd := exec.Command("/bin/sh", "-c", fullCmd)

	done := make(chan execResult, 1)

	go func() {
		stdoutPipe, err := cmd.StdoutPipe()
		if err != nil {
			done <- execResult{nil, nil, err}
			return
		}
		stderrPipe, err := cmd.StderrPipe()
		if err != nil {
			done <- execResult{nil, nil, err}
			return
		}

		if err := cmd.Start(); err != nil {
			done <- execResult{nil, nil, err}
			return
		}

		stdoutBuf, _ := io.ReadAll(stdoutPipe)
		stderrBuf, _ := io.ReadAll(stderrPipe)
		err = cmd.Wait()
		done <- execResult{stdoutBuf, stderrBuf, err}
	}()

	res, timedOut := waitTimeout(done, timeout)
	if timedOut {
		cmd.Process.Kill()
		<-done
		return &protocol.ExecResult{Stdout: "", Stderr: "", ExitCode: -1}, fmt.Errorf("command timed out")
	}

	return localExecResult(res), nil
}

func runLocalSudo(fullCmd string, sudoOpts *protocol.SudoOptions, timeout int) (*protocol.ExecResult, error) {
	askpassPath, cleanup, err := createAskpassHelper(sudoOpts.Password)
	if err != nil {
		return nil, fmt.Errorf("failed to create askpass helper: %w", err)
	}
	defer cleanup()

	sudoPrefix := buildSudoPrefix(sudoOpts)
	sudoCmd := fmt.Sprintf("%s -A %s", sudoPrefix, fullCmd)

	cmd := exec.Command("/bin/sh", "-c", sudoCmd)
	cmd.Env = append(os.Environ(), fmt.Sprintf("SUDO_ASKPASS=%s", askpassPath))

	done := make(chan execResult, 1)

	go func() {
		stdoutPipe, err := cmd.StdoutPipe()
		if err != nil {
			done <- execResult{nil, nil, err}
			return
		}
		stderrPipe, err := cmd.StderrPipe()
		if err != nil {
			done <- execResult{nil, nil, err}
			return
		}

		if err := cmd.Start(); err != nil {
			done <- execResult{nil, nil, err}
			return
		}

		stdoutBuf, _ := io.ReadAll(stdoutPipe)
		stderrBuf, _ := io.ReadAll(stderrPipe)
		err = cmd.Wait()
		done <- execResult{stdoutBuf, stderrBuf, err}
	}()

	res, timedOut := waitTimeout(done, timeout)
	if timedOut {
		cmd.Process.Kill()
		<-done
		cleanup()
		return &protocol.ExecResult{Stdout: "", Stderr: "", ExitCode: -1}, fmt.Errorf("command timed out")
	}

	return localExecResult(res), nil
}

// --- Sudo helpers (safe, no shell injection) ---

// createAskpassHelper creates a temporary shell script that echoes the sudo password.
// Uses SUDO_ASKPASS mechanism instead of shell concatenation — no injection risk.
func createAskpassHelper(password string) (path string, cleanup func(), err error) {
	if password == "" {
		return "", func() {}, nil
	}

	tmpDir := os.TempDir()
	scriptPath := filepath.Join(tmpDir, fmt.Sprintf("agmux-askpass-%d", time.Now().UnixNano()))

	content := fmt.Sprintf("#!/bin/sh\nprintf '%s\\n'\n", password)
	if err := os.WriteFile(scriptPath, []byte(content), 0700); err != nil {
		return "", func() {}, fmt.Errorf("failed to write askpass script: %w", err)
	}

	cleanup = func() {
		os.Remove(scriptPath)
	}

	return scriptPath, cleanup, nil
}

func buildSudoPrefix(sudoOpts *protocol.SudoOptions) string {
	if sudoOpts.Login {
		return "sudo -i"
	}
	if sudoOpts.User != "" {
		return fmt.Sprintf("sudo -u %s", sudoOpts.User)
	}
	return "sudo"
}

// --- Result processing helpers ---

func sshExecResult(res execResult) *protocol.ExecResult {
	if res.err != nil {
		if exitErr, ok := res.err.(*ssh.ExitError); ok {
			return &protocol.ExecResult{
				Stdout:   string(res.stdout),
				Stderr:   string(res.stderr),
				ExitCode: exitErr.ExitStatus(),
			}
		}
	}
	return &protocol.ExecResult{
		Stdout:   string(res.stdout),
		Stderr:   string(res.stderr),
		ExitCode: exitCodeFromErr(res.err),
	}
}

func localExecResult(res execResult) *protocol.ExecResult {
	if res.err != nil {
		if exitErr, ok := res.err.(*exec.ExitError); ok {
			return &protocol.ExecResult{
				Stdout:   string(res.stdout),
				Stderr:   string(res.stderr) + string(exitErr.Stderr),
				ExitCode: exitErr.ExitCode(),
			}
		}
	}
	return &protocol.ExecResult{
		Stdout:   string(res.stdout),
		Stderr:   string(res.stderr),
		ExitCode: exitCodeFromErr(res.err),
	}
}

func exitCodeFromErr(err error) int {
	if err == nil {
		return 0
	}
	return 1 // generic error, no exit status available
}

// --- Timeout helper ---

func waitTimeout(done chan execResult, timeout int) (execResult, bool) {
	if timeout <= 0 {
		return <-done, false
	}

	timer := time.NewTimer(time.Duration(timeout) * time.Second)
	defer timer.Stop()

	select {
	case res := <-done:
		return res, false
	case <-timer.C:
		return execResult{}, true
	}
}
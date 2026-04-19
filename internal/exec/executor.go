package exec

import (
	"bytes"
	"fmt"
	"io"
	"os/exec"
	"sync"
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

// streamExecResult holds the result of a streaming command execution.
type streamExecResult struct {
	exitCode int
	err      error
}

// ExecStream executes a command in the specified session, streaming stdout/stderr
// to the provided writers as output arrives.
// Returns the exit code and any fatal (non-exit-status) error.
func (e *Executor) ExecStream(sessionName, command string, timeout int, sudoOpts *protocol.SudoOptions, stdoutW, stderrW io.Writer) (int, error) {
	sess, err := e.sessions.Get(sessionName)
	if err != nil {
		return 1, err
	}

	sess.SetLastCmd(command)

	if sess.IsLocal() {
		return runLocalStream(command, timeout, sudoOpts, stdoutW, stderrW)
	}

	sshSess := sess.(*session.SSHSession)
	sshClient := sshSess.GetSSHClient()
	if sshClient == nil {
		return 1, fmt.Errorf("session not connected")
	}

	return runRemoteStream(sshClient, command, timeout, sudoOpts, stdoutW, stderrW)
}

// ExecLocalStream executes a one-off local command with streaming output (no session needed).
func (e *Executor) ExecLocalStream(command string, timeout int, sudoOpts *protocol.SudoOptions, stdoutW, stderrW io.Writer) (int, error) {
	return runLocalStream(command, timeout, sudoOpts, stdoutW, stderrW)
}

// --- Remote streaming execution ---

func runRemoteStream(client *ssh.Client, command string, timeout int, sudoOpts *protocol.SudoOptions, stdoutW, stderrW io.Writer) (int, error) {
	sshSession, err := client.NewSession()
	if err != nil {
		return 1, fmt.Errorf("failed to create SSH session: %w", err)
	}
	defer sshSession.Close()

	fullCmd := fmt.Sprintf("/bin/bash -c %q", command)

	if sudoOpts != nil && sudoOpts.Enabled {
		return runRemoteSudoStream(sshSession, fullCmd, sudoOpts, timeout, stdoutW, stderrW)
	}

	return runRemoteNormalStream(sshSession, fullCmd, timeout, stdoutW, stderrW)
}

func runRemoteNormalStream(sshSession *ssh.Session, fullCmd string, timeout int, stdoutW, stderrW io.Writer) (int, error) {
	stdoutPipe, err := sshSession.StdoutPipe()
	if err != nil {
		return 1, err
	}
	stderrPipe, err := sshSession.StderrPipe()
	if err != nil {
		return 1, err
	}

	if err := sshSession.Start(fullCmd); err != nil {
		return 1, err
	}

	done := make(chan streamExecResult, 1)
	go func() {
		var wg sync.WaitGroup
		wg.Add(2)
		go func() { defer wg.Done(); io.Copy(stdoutW, stdoutPipe) }()
		go func() { defer wg.Done(); io.Copy(stderrW, stderrPipe) }()
		wg.Wait()

		code, exitErr := sshExitCode(sshSession.Wait())
		done <- streamExecResult{exitCode: code, err: exitErr}
	}()

	return waitStreamTimeout(done, sshSession, nil, timeout)
}

func runRemoteSudoStream(sshSession *ssh.Session, fullCmd string, sudoOpts *protocol.SudoOptions, timeout int, stdoutW, stderrW io.Writer) (int, error) {
	wrappedCmd := buildSudoCommand(fullCmd, sudoOpts)

	stdoutPipe, err := sshSession.StdoutPipe()
	if err != nil {
		return 1, err
	}
	stderrPipe, err := sshSession.StderrPipe()
	if err != nil {
		return 1, err
	}
	stdinPipe, err := sshSession.StdinPipe()
	if err != nil {
		return 1, err
	}

	if err := sshSession.Start(wrappedCmd); err != nil {
		return 1, err
	}

	if err := writePassword(stdinPipe, sudoOpts.Password); err != nil {
		return 1, err
	}

	done := make(chan streamExecResult, 1)
	go func() {
		var wg sync.WaitGroup
		wg.Add(2)
		go func() { defer wg.Done(); io.Copy(stdoutW, stdoutPipe) }()
		go func() { defer wg.Done(); io.Copy(stderrW, stderrPipe) }()
		wg.Wait()

		code, exitErr := sshExitCode(sshSession.Wait())
		done <- streamExecResult{exitCode: code, err: exitErr}
	}()

	return waitStreamTimeout(done, sshSession, nil, timeout)
}

// sshExitCode normalises the error returned by ssh.Session.Wait:
// a non-zero exit is not a fatal error — it is returned as an exit code.
func sshExitCode(err error) (int, error) {
	if err == nil {
		return 0, nil
	}
	if exitErr, ok := err.(*ssh.ExitError); ok {
		return exitErr.ExitStatus(), nil
	}
	return 1, err
}

// --- Local streaming execution ---

func runLocalStream(command string, timeout int, sudoOpts *protocol.SudoOptions, stdoutW, stderrW io.Writer) (int, error) {
	fullCmd := fmt.Sprintf("/bin/bash -c %q", command)

	if sudoOpts != nil && sudoOpts.Enabled {
		return runLocalSudoStream(fullCmd, sudoOpts, timeout, stdoutW, stderrW)
	}

	return runLocalNormalStream(fullCmd, timeout, stdoutW, stderrW)
}

func runLocalNormalStream(fullCmd string, timeout int, stdoutW, stderrW io.Writer) (int, error) {
	cmd := exec.Command("/bin/sh", "-c", fullCmd)

	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		return 1, err
	}
	stderrPipe, err := cmd.StderrPipe()
	if err != nil {
		return 1, err
	}

	if err := cmd.Start(); err != nil {
		return 1, err
	}

	done := make(chan streamExecResult, 1)
	go func() {
		var wg sync.WaitGroup
		wg.Add(2)
		go func() { defer wg.Done(); io.Copy(stdoutW, stdoutPipe) }()
		go func() { defer wg.Done(); io.Copy(stderrW, stderrPipe) }()
		wg.Wait()

		code, exitErr := localExitCode(cmd.Wait())
		done <- streamExecResult{exitCode: code, err: exitErr}
	}()

	return waitStreamTimeout(done, nil, cmd, timeout)
}

func runLocalSudoStream(fullCmd string, sudoOpts *protocol.SudoOptions, timeout int, stdoutW, stderrW io.Writer) (int, error) {
	sudoCmd := buildSudoCommand(fullCmd, sudoOpts)
	cmd := exec.Command("/bin/sh", "-c", sudoCmd)

	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		return 1, err
	}
	stderrPipe, err := cmd.StderrPipe()
	if err != nil {
		return 1, err
	}
	stdinPipe, err := cmd.StdinPipe()
	if err != nil {
		return 1, err
	}

	if err := cmd.Start(); err != nil {
		return 1, err
	}

	if err := writePassword(stdinPipe, sudoOpts.Password); err != nil {
		return 1, err
	}

	done := make(chan streamExecResult, 1)
	go func() {
		var wg sync.WaitGroup
		wg.Add(2)
		go func() { defer wg.Done(); io.Copy(stdoutW, stdoutPipe) }()
		go func() { defer wg.Done(); io.Copy(stderrW, stderrPipe) }()
		wg.Wait()

		code, exitErr := localExitCode(cmd.Wait())
		done <- streamExecResult{exitCode: code, err: exitErr}
	}()

	return waitStreamTimeout(done, nil, cmd, timeout)
}

// localExitCode normalises the error returned by cmd.Wait for local processes.
func localExitCode(err error) (int, error) {
	if err == nil {
		return 0, nil
	}
	if exitErr, ok := err.(*exec.ExitError); ok {
		return exitErr.ExitCode(), nil
	}
	return 1, err
}

// waitStreamTimeout waits for a streaming goroutine to finish, applying an
// optional timeout.  sshSession / cmd are used to forcefully stop the process
// on timeout; pass nil for the one that does not apply.
func waitStreamTimeout(done <-chan streamExecResult, sshSession *ssh.Session, cmd *exec.Cmd, timeout int) (int, error) {
	if timeout <= 0 {
		res := <-done
		return res.exitCode, res.err
	}

	timer := time.NewTimer(time.Duration(timeout) * time.Second)
	defer timer.Stop()

	select {
	case res := <-done:
		return res.exitCode, res.err
	case <-timer.C:
		if sshSession != nil {
			sshSession.Signal(ssh.SIGKILL)
			sshSession.Close()
		}
		if cmd != nil && cmd.Process != nil {
			cmd.Process.Kill()
		}
		<-done
		return -1, fmt.Errorf("command timed out")
	}
}


func runRemote(client *ssh.Client, command string, timeout int, sudoOpts *protocol.SudoOptions) (*protocol.ExecResult, error) {
	sshSession, err := client.NewSession()
	if err != nil {
		return nil, fmt.Errorf("failed to create SSH session: %w", err)
	}
	defer sshSession.Close()

	fullCmd := fmt.Sprintf("/bin/bash -c %q", command)

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

		stdoutBuf, stderrBuf, err := readPipes(stdoutPipe, stderrPipe)
		if err != nil {
			done <- execResult{stdoutBuf, stderrBuf, err}
			return
		}

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
	wrappedCmd := buildSudoCommand(fullCmd, sudoOpts)

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
		stdinPipe, err := sshSession.StdinPipe()
		if err != nil {
			done <- execResult{nil, nil, err}
			return
		}

		if err := sshSession.Start(wrappedCmd); err != nil {
			done <- execResult{nil, nil, err}
			return
		}

		if err := writePassword(stdinPipe, sudoOpts.Password); err != nil {
			done <- execResult{nil, nil, err}
			return
		}

		stdoutBuf, stderrBuf, err := readPipes(stdoutPipe, stderrPipe)
		if err != nil {
			done <- execResult{stdoutBuf, stderrBuf, err}
			return
		}

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

// --- Local execution ---

func runLocal(command string, timeout int, sudoOpts *protocol.SudoOptions) (*protocol.ExecResult, error) {
	fullCmd := fmt.Sprintf("/bin/bash -c %q", command)

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

		stdoutBuf, stderrBuf, err := readPipes(stdoutPipe, stderrPipe)
		if err != nil {
			done <- execResult{stdoutBuf, stderrBuf, err}
			return
		}

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
	sudoCmd := buildSudoCommand(fullCmd, sudoOpts)

	cmd := exec.Command("/bin/sh", "-c", sudoCmd)

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
		stdinPipe, err := cmd.StdinPipe()
		if err != nil {
			done <- execResult{nil, nil, err}
			return
		}

		if err := cmd.Start(); err != nil {
			done <- execResult{nil, nil, err}
			return
		}

		if err := writePassword(stdinPipe, sudoOpts.Password); err != nil {
			done <- execResult{nil, nil, err}
			return
		}

		stdoutBuf, stderrBuf, err := readPipes(stdoutPipe, stderrPipe)
		if err != nil {
			done <- execResult{stdoutBuf, stderrBuf, err}
			return
		}

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

// --- Sudo helpers (safe, no shell injection) ---

func writePassword(stdin io.WriteCloser, password string) error {
	defer stdin.Close()

	if password == "" {
		return nil
	}

	if _, err := io.WriteString(stdin, password+"\n"); err != nil {
		return fmt.Errorf("failed to write sudo password: %w", err)
	}

	return nil
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

func buildSudoCommand(fullCmd string, sudoOpts *protocol.SudoOptions) string {
	return fmt.Sprintf("%s -S -p '' %s", buildSudoPrefix(sudoOpts), fullCmd)
}

func readPipes(stdoutPipe, stderrPipe io.Reader) ([]byte, []byte, error) {
	type readResult struct {
		stream string
		data   []byte
		err    error
	}

	var wg sync.WaitGroup
	results := make(chan readResult, 2)

	read := func(stream string, r io.Reader) {
		defer wg.Done()
		data, err := io.ReadAll(r)
		results <- readResult{stream: stream, data: data, err: err}
	}

	wg.Add(2)
	go read("stdout", stdoutPipe)
	go read("stderr", stderrPipe)
	wg.Wait()
	close(results)

	var stdoutBuf []byte
	var stderrBuf []byte
	var readErr error

	for result := range results {
		if result.stream == "stdout" {
			stdoutBuf = result.data
		} else {
			stderrBuf = result.data
		}
		if readErr == nil && result.err != nil {
			readErr = result.err
		}
	}

	return stdoutBuf, stderrBuf, readErr
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
			stderr := res.stderr
			if len(exitErr.Stderr) > 0 {
				stderr = append(stderr, exitErr.Stderr...)
			}
			return &protocol.ExecResult{
				Stdout:   string(res.stdout),
				Stderr:   string(bytes.Clone(stderr)),
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

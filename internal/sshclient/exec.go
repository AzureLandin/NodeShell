package sshclient

import (
	"context"
	"errors"
	"io"
	"time"

	"golang.org/x/crypto/ssh"

	"nodeshell/internal/apperror"
)

// MaxExecBytes is the hard cap on stdout collected from one remote exec
// (mirrors the Electron build's MAX_EXEC_BYTES = 2 * 1024 * 1024).
const MaxExecBytes = 2 * 1024 * 1024

// execResult carries one exec outcome.
type execResult struct {
	out string
	err error
}

// Exec runs a non-interactive remote command over a fresh session channel —
// never the interactive PTY session — and returns its stdout. stdout is hard
// capped at MaxExecBytes: an overflow closes the exec channel and returns a
// coded UNKNOWN error without command/stdout/path details. stderr is drained
// with bounded memory; the raw stderr (which may embed secrets) is never
// surfaced, only a generic message. ctx cancellation maps to CANCELLED, the
// timeout to TIMEOUT.
func (s *Session) Exec(ctx context.Context, command string, timeout time.Duration) (string, error) {
	s.mu.Lock()
	closed := s.closed
	s.mu.Unlock()
	if closed {
		return "", &Error{Code: apperror.Unknown, Message: "Session is closed"}
	}
	execCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	res := s.runExec(execCtx, command)
	return res.out, res.err
}

// runExec opens one exec session and gathers stdout/stderr/exit, staying
// responsive to ctx at every blocking step. On cancellation the exec session
// is closed to unblock the in-flight Run; the run/stdout/stderr goroutines
// settle on the closed channel (released, not synchronously joined).
func (s *Session) runExec(ctx context.Context, command string) execResult {
	sshSess, err := openExecSession(ctx, s.client)
	if err != nil {
		return execResult{err: err}
	}
	defer func() { _ = sshSess.Close() }()

	stdout, err := sshSess.StdoutPipe()
	if err != nil {
		return execResult{err: mapExecError(err)}
	}
	stderr, err := sshSess.StderrPipe()
	if err != nil {
		return execResult{err: mapExecError(err)}
	}

	// x/crypto has no context-aware Run: it runs in a goroutine and the
	// select loop below closes the exec session on ctx, which unblocks it.
	runErrCh := make(chan error, 1)
	go func() { runErrCh <- sshSess.Run(command) }()
	stdoutCh := make(chan cappedResult, 1)
	go func() { stdoutCh <- readCapped(stdout, MaxExecBytes) }()
	stderrCh := make(chan error, 1)
	go func() { stderrCh <- drainBounded(stderr) }()

	var runErr error
	var stdoutRes cappedResult
	var stderrErr error
	var haveRun, haveStdout, haveStderr bool
	for {
		select {
		case runErr = <-runErrCh:
			haveRun = true
		case stdoutRes = <-stdoutCh:
			haveStdout = true
			if stdoutRes.overflow {
				// Hard cap exceeded: close the exec channel promptly so the
				// remote never streams unbounded data, then return a generic
				// coded error (no command/stdout/path in the message).
				_ = sshSess.Close()
				return execResult{err: &Error{Code: apperror.Unknown, Message: "Remote command output exceeded the limit"}}
			}
		case stderrErr = <-stderrCh:
			haveStderr = true
		case <-ctx.Done():
			_ = sshSess.Close()
			return execResult{err: codedCtxError(ctx)}
		}
		if haveRun && haveStdout && haveStderr {
			break
		}
	}
	if stdoutRes.err != nil {
		return execResult{err: mapExecError(stdoutRes.err)}
	}
	if stderrErr != nil {
		return execResult{err: mapExecError(stderrErr)}
	}
	// Electron semantics: a non-zero exit with empty stdout is an error, but
	// the raw stderr (which may embed secrets) is never returned — only a
	// generic message. A non-zero exit WITH stdout resolves with the stdout.
	var exitErr *ssh.ExitError
	if errors.As(runErr, &exitErr) && exitErr.ExitStatus() != 0 && len(stdoutRes.data) == 0 {
		return execResult{err: &Error{Code: apperror.Unknown, Message: "Remote command failed"}}
	}
	if runErr != nil && !errors.As(runErr, &exitErr) {
		return execResult{err: mapExecError(runErr)}
	}
	return execResult{out: string(stdoutRes.data)}
}

// openExecSession opens a session channel, staying responsive to ctx: a
// stalled channel-open can only be aborted by closing the whole connection
// (which would kill the PTY), so when ctx wins first the result is drained
// by a cleanup goroutine that closes any session which still resolves — a
// channel-open confirmed after the timeout never leaks a session channel
// until the connection closes. The cleanup goroutine and the blocked
// NewSession share a lifetime: if NewSession never returns, both are
// released when the connection closes. Exec never waits on them.
func openExecSession(ctx context.Context, client *ssh.Client) (*ssh.Session, error) {
	type result struct {
		sess *ssh.Session
		err  error
	}
	ch := make(chan result, 1)
	go func() {
		sess, err := client.NewSession()
		ch <- result{sess, err}
	}()
	select {
	case <-ctx.Done():
		// Drain the in-flight NewSession result in the background: a channel
		// open confirmed after we already gave up must be closed, never left
		// to pin a session channel until the connection closes. The drain
		// reads exactly one result and settles when NewSession returns.
		go func() {
			res := <-ch
			if res.sess != nil {
				_ = res.sess.Close()
			}
		}()
		return nil, codedCtxError(ctx)
	case res := <-ch:
		if res.err != nil {
			return nil, mapExecError(res.err)
		}
		return res.sess, nil
	}
}

// codedCtxError maps a context outcome to a coded exec error.
func codedCtxError(ctx context.Context) error {
	if errors.Is(ctx.Err(), context.Canceled) {
		return &Error{Code: apperror.Cancelled, Message: "Command cancelled"}
	}
	return &Error{Code: apperror.Timeout, Message: "Command timed out"}
}

// cappedResult is the outcome of reading a stream up to its cap.
type cappedResult struct {
	data     []byte
	overflow bool
	err      error
}

// readCapped reads r until EOF or cap bytes; overflow reports that the stream
// held more than cap bytes (the read stops at the cap so the caller can close
// the channel promptly).
func readCapped(r io.Reader, cap int) cappedResult {
	buf := make([]byte, 0, 4096)
	tmp := make([]byte, 32*1024)
	for {
		n, err := r.Read(tmp)
		if n > 0 {
			if len(buf)+n > cap {
				return cappedResult{data: buf, overflow: true}
			}
			buf = append(buf, tmp[:n]...)
		}
		if err != nil {
			if err == io.EOF {
				return cappedResult{data: buf}
			}
			return cappedResult{data: buf, err: err}
		}
	}
}

// drainBounded reads r with a fixed-size buffer, discarding everything, so a
// noisy stderr never exhausts memory nor blocks the remote (the channel keeps
// draining even beyond the stdout cap).
func drainBounded(r io.Reader) error {
	tmp := make([]byte, 32*1024)
	for {
		_, err := r.Read(tmp)
		if err != nil {
			if err == io.EOF {
				return nil
			}
			return err
		}
	}
}

// mapExecError collapses a non-exit exec failure to a coded UNKNOWN without
// leaking command/stdout/path details.
func mapExecError(err error) error {
	if err == nil {
		return nil
	}
	var exitErr *ssh.ExitError
	if errors.As(err, &exitErr) {
		return nil // exit-status is handled by the caller
	}
	if errors.Is(err, io.EOF) {
		return &Error{Code: apperror.Unknown, Message: "Connection closed"}
	}
	return &Error{Code: apperror.Unknown, Message: "Remote command failed"}
}

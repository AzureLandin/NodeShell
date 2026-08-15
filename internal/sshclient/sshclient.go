// Package sshclient establishes a single SSH connection with an interactive
// xterm-256color PTY session. It depends only on a narrow known-hosts
// interface and a dial seam — the Wails event system is wired by the sessions
// package on top of it.
package sshclient

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"

	"nodeshell/internal/apperror"
	"nodeshell/internal/knownhosts"
)

// Timeouts mirror the Electron build's SSH_READY_TIMEOUT_MS and
// SSH_HARD_TIMEOUT_MS.
const (
	// DialTimeout is the nominal connection timeout (10s): dial + handshake.
	DialTimeout = 10 * time.Second
	// HardDeadline is the application hard abort slightly above DialTimeout,
	// so a stalled handshake can never hang the caller indefinitely.
	HardDeadline = DialTimeout + 500*time.Millisecond
	// DefaultCols / DefaultRows are the initial PTY size.
	DefaultCols = 80
	DefaultRows = 24
)

// Dialer is the TCP dial seam. The production default is
// net.Dialer.DialContext, whose DialContext provides true cancellation of the
// TCP connect; tests inject a fake or a counting dialer.
type Dialer func(ctx context.Context, network, addr string) (net.Conn, error)

// HostKeys is the narrow known-hosts dependency of a connection: a store of
// SHA256 base64 host-key fingerprints keyed by "host:port". The production
// internal/knownhosts.Store satisfies it.
type HostKeys interface {
	Check(host string, port int, fingerprint string) (knownhosts.CheckResult, error)
	Remember(host string, port int, fingerprint string) error
}

// Options configures one connection attempt. Secrets never end up in error
// messages or log lines.
type Options struct {
	Host       string
	Port       int
	Username   string
	AuthMethod string // "password" or "privateKey"
	// Password is offered as a password and keyboard-interactive answer.
	Password string
	// PrivateKey holds key CONTENT (from the OS keyring). When empty and
	// PrivateKeyPath is set, KeyReader reads the path (home-boundary checked).
	PrivateKey     string
	PrivateKeyPath string
	KeyReader      func(path string) (string, error)
	AcceptHostKey  bool
	Cols, Rows     int
	HostKeys       HostKeys
	Dialer         Dialer
	// Deadline caps the whole dial+handshake. Zero uses HardDeadline (10.5s);
	// tests shorten it.
	Deadline time.Duration
}

// Session is an established interactive SSH session with a PTY and shell.
// Close is idempotent; the reader streams (Stdout/Stderr) unblock with an
// error once the connection is closed.
type Session struct {
	client      *ssh.Client
	sshSess     *ssh.Session
	stdin       io.WriteCloser
	stdout      io.Reader
	stderr      io.Reader
	fingerprint string

	mu     sync.Mutex
	closed bool
}

// Connect establishes an SSH connection: TCP dial, host-key verification
// before any credential exchange, authentication, PTY request and shell
// start. The whole dial+handshake+auth+pty+shell sequence is bounded by the
// hard deadline; cancelling ctx aborts it for real by closing the underlying
// net.Conn (x/crypto/ssh has no context-aware handshake).
func Connect(ctx context.Context, opts Options) (*Session, error) {
	if opts.HostKeys == nil {
		return nil, &Error{Code: apperror.Unknown, Message: "Host key store is unavailable"}
	}
	deadline := opts.Deadline
	if deadline <= 0 {
		deadline = HardDeadline
	}
	ctx, cancel := context.WithDeadline(ctx, time.Now().Add(deadline))
	defer cancel()

	addr := net.JoinHostPort(opts.Host, strconv.Itoa(opts.Port))
	dialer := opts.Dialer
	if dialer == nil {
		dialer = (&net.Dialer{Timeout: DialTimeout}).DialContext
	}
	conn, err := dialer(ctx, "tcp", addr)
	if err != nil {
		return nil, mapDialError(ctx, err)
	}

	auth, err := authMethods(opts)
	if err != nil {
		_ = conn.Close()
		return nil, err
	}
	var acceptedFingerprint string
	config := &ssh.ClientConfig{
		User:    opts.Username,
		Auth:    auth,
		Timeout: DialTimeout,
		// HostKeyCallback is mandatory: the library refuses a nil callback,
		// and InsecureIgnoreHostKey is never used. The callback runs during
		// key exchange, before any credential is sent.
		HostKeyCallback: hostKeyCallback(opts, &acceptedFingerprint),
	}
	// x/crypto v0.51 only negotiates the "none" compression algorithm, so
	// compression is off by construction; SetDefaults still applies the
	// remaining defaults (ciphers, KEX, MACs, random source).
	config.Config.SetDefaults()

	type handshakeResult struct {
		client *ssh.Client
		err    error
	}
	result := make(chan handshakeResult, 1)
	go func() {
		sshConn, chans, reqs, err := ssh.NewClientConn(conn, addr, config)
		if err != nil {
			result <- handshakeResult{err: err}
			return
		}
		result <- handshakeResult{client: ssh.NewClient(sshConn, chans, reqs)}
	}()

	var client *ssh.Client
	select {
	case <-ctx.Done():
		// Closing the raw connection unblocks the in-flight handshake; the
		// library has no context-aware entry point.
		_ = conn.Close()
		<-result // let the handshake goroutine finish
		if errors.Is(ctx.Err(), context.Canceled) {
			return nil, &Error{Code: apperror.Cancelled, Message: "Connection cancelled"}
		}
		return nil, &Error{Code: apperror.Timeout, Message: "Connection timed out"}
	case res := <-result:
		if res.err != nil {
			return nil, mapHandshakeError(ctx, res.err)
		}
		client = res.client
	}

	cols, rows := opts.Cols, opts.Rows
	if cols <= 0 {
		cols = DefaultCols
	}
	if rows <= 0 {
		rows = DefaultRows
	}
	// The deadline covers the PTY+shell phase too: openShell runs in a
	// goroutine so a stalled NewSession/RequestPty is aborted for real by
	// closing the client (x/crypto has no context-aware entry points), then
	// the goroutine is joined so nothing leaks.
	type shellResult struct {
		sess *Session
		err  error
	}
	shellRes := make(chan shellResult, 1)
	go func() {
		sess, err := openShell(client, cols, rows)
		shellRes <- shellResult{sess, err}
	}()
	var sess *Session
	select {
	case <-ctx.Done():
		_ = client.Close()
		<-shellRes // join the openShell goroutine
		if errors.Is(ctx.Err(), context.Canceled) {
			return nil, &Error{Code: apperror.Cancelled, Message: "Connection cancelled"}
		}
		return nil, &Error{Code: apperror.Timeout, Message: "Connection timed out"}
	case res := <-shellRes:
		if res.err != nil {
			_ = client.Close()
			return nil, res.err
		}
		sess = res.sess
	}
	sess.fingerprint = acceptedFingerprint
	return sess, nil
}

// openShell requests the PTY and starts the interactive shell. Both stdout
// and stderr pipes are created so callers can safely merge the two streams
// while preserving per-stream order.
func openShell(client *ssh.Client, cols, rows int) (*Session, error) {
	sshSess, err := client.NewSession()
	if err != nil {
		return nil, &Error{Code: apperror.Unknown, Message: "Failed to open session channel"}
	}
	modes := ssh.TerminalModes{
		ssh.ECHO:          1,
		ssh.TTY_OP_ISPEED: 14400,
		ssh.TTY_OP_OSPEED: 14400,
	}
	if err := sshSess.RequestPty("xterm-256color", rows, cols, modes); err != nil {
		_ = sshSess.Close()
		return nil, &Error{Code: apperror.Unknown, Message: "Failed to request terminal"}
	}
	stdin, err := sshSess.StdinPipe()
	if err != nil {
		_ = sshSess.Close()
		return nil, &Error{Code: apperror.Unknown, Message: "Failed to open stdin channel"}
	}
	stdout, err := sshSess.StdoutPipe()
	if err != nil {
		_ = sshSess.Close()
		return nil, &Error{Code: apperror.Unknown, Message: "Failed to open stdout channel"}
	}
	stderr, err := sshSess.StderrPipe()
	if err != nil {
		_ = sshSess.Close()
		return nil, &Error{Code: apperror.Unknown, Message: "Failed to open stderr channel"}
	}
	if err := sshSess.Shell(); err != nil {
		_ = sshSess.Close()
		return nil, &Error{Code: apperror.Unknown, Message: "Failed to start shell"}
	}
	return &Session{client: client, sshSess: sshSess, stdin: stdin, stdout: stdout, stderr: stderr}, nil
}

// Write sends data to the remote stdin.
func (s *Session) Write(p []byte) (int, error) {
	return s.stdin.Write(p)
}

// Resize sends an SSH window-change request. cols/rows must be positive.
func (s *Session) Resize(cols, rows int) error {
	if cols <= 0 || rows <= 0 {
		return &Error{Code: apperror.Unknown, Message: "Invalid terminal size"}
	}
	return s.sshSess.WindowChange(rows, cols)
}

// Wait blocks until the remote session ends (remote close, exit, or a
// transport failure). It returns the remote exit status on a clean end and a
// transport error when the connection drops.
func (s *Session) Wait() error {
	return s.sshSess.Wait()
}

// Close tears down the connection. Idempotent and safe from any goroutine;
// concurrent callers all return once.
func (s *Session) Close() error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	s.mu.Unlock()
	return s.client.Close()
}

// Stdout returns the PTY stdout stream.
func (s *Session) Stdout() io.Reader { return s.stdout }

// Stderr returns the PTY stderr stream (normally empty once a PTY merges
// both streams server-side, but present for safety).
func (s *Session) Stderr() io.Reader { return s.stderr }

// Fingerprint returns the SHA256 base64 fingerprint of the host key that was
// verified during this connection, or "" when the handshake never completed.
func (s *Session) Fingerprint() string { return s.fingerprint }

// Dial opens a TCP connection through this session (SSH direct-tcpip), used
// for local port forwards. The address is the remote endpoint, typically
// 127.0.0.1:port.
func (s *Session) Dial(network, addr string) (net.Conn, error) {
	s.mu.Lock()
	closed := s.closed
	client := s.client
	s.mu.Unlock()
	if closed || client == nil {
		return nil, &Error{Code: apperror.Unknown, Message: "Session is closed"}
	}
	conn, err := client.Dial(network, addr)
	if err != nil {
		return nil, mapForwardError(err)
	}
	return conn, nil
}

// authMethods builds the ssh.AuthMethod list for the requested auth method.
// Password auth offers both password and keyboard-interactive (servers often
// advertise only the latter); private-key auth parses the key content, never
// a path.
func authMethods(opts Options) ([]ssh.AuthMethod, error) {
	switch opts.AuthMethod {
	case "password":
		var methods []ssh.AuthMethod
		if opts.Password != "" {
			methods = append(methods, ssh.Password(opts.Password))
		}
		methods = append(methods, ssh.KeyboardInteractive(keyboardInteractive(opts.Password)))
		return methods, nil
	case "privateKey":
		content := opts.PrivateKey
		if content == "" && opts.PrivateKeyPath != "" {
			if opts.KeyReader == nil {
				return nil, &Error{Code: apperror.AuthFailed, Message: "Private key reader is unavailable"}
			}
			read, err := opts.KeyReader(opts.PrivateKeyPath)
			if err != nil {
				return nil, &Error{Code: apperror.AuthFailed, Message: "Failed to read private key"}
			}
			content = read
		}
		if content == "" {
			return nil, &Error{Code: apperror.AuthFailed, Message: "Private key is missing"}
		}
		signer, err := ssh.ParsePrivateKey([]byte(content))
		if err != nil {
			var missing *ssh.PassphraseMissingError
			if errors.As(err, &missing) {
				// No passphrase UI exists; a passphrase-protected key is a
				// clear AUTH_FAILED rather than a silent hang or retry.
				return nil, &Error{Code: apperror.AuthFailed, Message: "Encrypted private key requires a passphrase"}
			}
			return nil, &Error{Code: apperror.AuthFailed, Message: "Failed to parse private key"}
		}
		return []ssh.AuthMethod{ssh.PublicKeys(signer)}, nil
	default:
		return nil, &Error{Code: apperror.AuthFailed, Message: "Unsupported authentication method"}
	}
}

// keyboardInteractive answers with the password whenever the prompt mentions
// a password or is not echoed (the Electron build's rule); every other prompt
// — TOTP, challenge codes, informational — is answered empty, so the
// credential is never sent to an echoed non-password challenge. A short echos
// array never panics: a missing entry counts as not echoed.
func keyboardInteractive(password string) ssh.KeyboardInteractiveChallenge {
	return func(_, _ string, questions []string, echos []bool) ([]string, error) {
		answers := make([]string, len(questions))
		for i, q := range questions {
			echo := i < len(echos) && echos[i]
			if echo && !strings.Contains(strings.ToLower(q), "password") {
				continue
			}
			answers[i] = password
		}
		return answers, nil
	}
}

// hostKeyCallback verifies the host key before authentication and records the
// accepted fingerprint for the post-connect Remember step.
func hostKeyCallback(opts Options, accepted *string) ssh.HostKeyCallback {
	return func(hostname string, remote net.Addr, key ssh.PublicKey) error {
		fp := Fingerprint(key)
		result, err := opts.HostKeys.Check(opts.Host, opts.Port, fp)
		if err != nil {
			return mapCheckError(err)
		}
		switch result.Status {
		case "ok":
			*accepted = fp
			return nil
		case "changed":
			if opts.AcceptHostKey {
				*accepted = fp
				return nil
			}
			return &Error{
				Code:        apperror.HostKeyChanged,
				Message:     fmt.Sprintf("Host key has changed (was SHA256:%s, now SHA256:%s)", result.Previous, fp),
				Fingerprint: fp,
				Previous:    result.Previous,
			}
		default:
			if opts.AcceptHostKey {
				*accepted = fp
				return nil
			}
			return &Error{
				Code:        apperror.HostKeyUnknown,
				Message:     fmt.Sprintf("Unknown host key (SHA256:%s)", fp),
				Fingerprint: fp,
			}
		}
	}
}

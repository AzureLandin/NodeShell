// Package sshtest provides a controllable real SSH server (x/crypto/ssh)
// bound to 127.0.0.1 on a random port, used by the sshclient and sessions
// integration tests. It exercises the real wire protocol — authentication
// callbacks, session channels, pty-req/window-change/shell requests, stdin
// echo, exit-status and remote close — never a mock.
package sshtest

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"io"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pkg/sftp"

	"golang.org/x/crypto/ssh"
)

// Server is a single-host SSH test server.
type Server struct {
	ln       net.Listener
	config   *ssh.ServerConfig
	hostKey  ssh.PublicKey
	hostKeys []ssh.Signer
	mu       sync.Mutex

	// Addr is the "127.0.0.1:port" dial target.
	Addr string

	// StallHandshake, when true, accepts TCP connections but never completes
	// the SSH handshake, so clients hang until their own timeout/cancel.
	StallHandshake bool

	// StallSessionOpen, when true, completes the handshake and auth but never
	// answers the session channel-open, so the client's NewSession hangs until
	// its own deadline/cancel aborts the connection.
	StallSessionOpen bool

	// sessionOpenDelay, when > 0, delays answering each session channel-open
	// by this duration — the open then SUCCEEDS (unlike StallSessionOpen,
	// which never answers). Tests use it to make a client's NewSession resolve
	// after the client already timed out, then assert the client closes the
	// late session. Mutex-guarded; set via SetSessionOpenDelay.
	sessionOpenDelay time.Duration

	// StallPTY, when true, accepts the session channel but never reads its
	// requests, so the client's pty-req stays unanswered and RequestPty hangs
	// until the client aborts.
	StallPTY bool

	// KeyboardQuestions / KeyboardEchos, when set, override the challenge sent
	// to the client (default ["Password: ", "Verification code: "] /
	// [false, false]) so tests can exercise short echos arrays.
	KeyboardQuestions []string
	KeyboardEchos     []bool

	// PasswordOK, when set, accepts password auth iff it returns true.
	PasswordOK func(user, pass string) bool
	// KeyboardOK, when set, accepts keyboard-interactive auth. questions/
	// echos are the challenge the client received; answers are the replies.
	KeyboardOK func(user string, questions []string, echos []bool, answers []string) bool
	// PublicKeyOK, when set, accepts public-key auth for the key.
	PublicKeyOK func(user string, key ssh.PublicKey) bool

	// authAttempts counts every authentication callback fired (any method),
	// so tests can prove that a rejected host key prevented auth.
	authAttempts int

	// OnShell, when set, is called once per shell session with the channel
	// and request stream. The default handler echoes stdin to stdout and
	// exits 0. Tests use it to emit output (incl. split UTF-8), echo stderr,
	// stall, or close remotely.
	OnShell func(ch ssh.Channel, reqs <-chan *ssh.Request)

	// OnExec, when set, routes "exec" session requests to the handler with the
	// parsed command (the request is replied ok first). Other session requests
	// (pty-req/shell) still route to OnShell or the default echo shell, so a
	// server can serve both a terminal and exec channels on one connection.
	OnExec func(ch ssh.Channel, reqs <-chan *ssh.Request, command string)

	// sftpRoot, when set, makes every session channel answer the "sftp"
	// subsystem with a real pkg/sftp server rooted at the directory. Shell
	// requests on the same channel are refused (an SFTP client opens its own
	// session channel). OnShell is ignored while sftpRoot is set.
	sftpRoot string
	// sftpReadOnly, when set alongside sftpRoot, rejects every remote write
	// (used to test upload/download failure paths).
	sftpReadOnly bool

	// totalSessionsOpened counts session channels the server has confirmed
	// (accepted); activeSessions counts those currently being served. Tests
	// use them to prove a delayed channel-open was answered on the wire and
	// then closed by the client.
	totalSessionsOpened atomic.Int64
	activeSessions      atomic.Int64
}

// New starts a server on 127.0.0.1:0 and returns it. Every server has a
// freshly generated host key, so two servers never share a key (host-key
// tests control the trust store themselves).
func New(t *testing.T) *Server {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("sshtest: generate host key: %v", err)
	}
	signer, err := ssh.NewSignerFromKey(key)
	if err != nil {
		t.Fatalf("sshtest: host key signer: %v", err)
	}
	config := &ssh.ServerConfig{}
	config.AddHostKey(signer)
	s := &Server{config: config, hostKey: signer.PublicKey(), hostKeys: []ssh.Signer{signer}}
	config.PasswordCallback = func(c ssh.ConnMetadata, pass []byte) (*ssh.Permissions, error) {
		s.mu.Lock()
		s.authAttempts++
		s.mu.Unlock()
		if s.PasswordOK != nil && s.PasswordOK(c.User(), string(pass)) {
			return nil, nil
		}
		return nil, errAuth("password")
	}
	config.KeyboardInteractiveCallback = func(c ssh.ConnMetadata, client ssh.KeyboardInteractiveChallenge) (*ssh.Permissions, error) {
		s.mu.Lock()
		s.authAttempts++
		s.mu.Unlock()
		// Always run the challenge (INFO_REQUEST/INFO_RESPONSE) so the
		// client's keyboard-interactive state machine stays in sync, even
		// when the test has no KeyboardOK expectation.
		questions := s.KeyboardQuestions
		if questions == nil {
			questions = []string{"Password: ", "Verification code: "}
		}
		echos := s.KeyboardEchos
		if echos == nil {
			echos = []bool{false, false}
		}
		answers, err := client("user", "", questions, echos)
		if err != nil {
			return nil, err
		}
		if s.KeyboardOK != nil && s.KeyboardOK(c.User(), questions, echos, answers) {
			return nil, nil
		}
		return nil, errAuth("keyboard-interactive")
	}
	config.PublicKeyCallback = func(c ssh.ConnMetadata, key ssh.PublicKey) (*ssh.Permissions, error) {
		s.mu.Lock()
		s.authAttempts++
		s.mu.Unlock()
		if s.PublicKeyOK != nil && s.PublicKeyOK(c.User(), key) {
			return nil, nil
		}
		return nil, errAuth("publickey")
	}

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("sshtest: listen: %v", err)
	}
	s.ln = ln
	s.Addr = ln.Addr().String()
	go s.acceptLoop()
	t.Cleanup(func() { _ = ln.Close() })
	return s
}

// AuthAttempts returns how many authentication callbacks have fired.
func (s *Server) AuthAttempts() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.authAttempts
}

// SetSessionOpenDelay makes every subsequent session channel-open wait d
// before the server answers it — the open still succeeds (unlike
// StallSessionOpen, which never answers). Tests use it to make a client's
// NewSession resolve AFTER the client already timed out, then assert the
// client closes the late session. Race-safe to call while the server runs.
func (s *Server) SetSessionOpenDelay(d time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sessionOpenDelay = d
}

// ActiveSessions returns how many session channels are currently accepted
// and being served; a leaked session channel keeps it above the terminal
// baseline until the connection closes.
func (s *Server) ActiveSessions() int { return int(s.activeSessions.Load()) }

// TotalSessionsOpened returns how many session channels the server has
// confirmed so far (monotonic), so a test can wait on it to prove that a
// delayed channel-open was eventually answered on the wire.
func (s *Server) TotalSessionsOpened() int { return int(s.totalSessionsOpened.Load()) }

// HostKeyFingerprint returns the SHA256 base64 fingerprint of the server's
// host key (same computation as sshclient.Fingerprint), so tests can
// pre-seed a known-hosts store.
func (s *Server) HostKeyFingerprint() string {
	sum := sha256.Sum256(s.hostKey.Marshal())
	return base64.StdEncoding.EncodeToString(sum[:])
}

// SetHostKey replaces the server host key (host-key-changed tests). Existing
// connections are unaffected; new connections present the new key.
func (s *Server) SetHostKey() {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return
	}
	signer, err := ssh.NewSignerFromKey(key)
	if err != nil {
		return
	}
	s.mu.Lock()
	s.hostKey = signer.PublicKey()
	s.hostKeys = []ssh.Signer{signer}
	s.config.AddHostKey(signer) // replaces the old key (same type)
	s.mu.Unlock()
}

// EnableSFTP makes the server answer the "sftp" subsystem on session
// channels with a real pkg/sftp server rooted at rootDir. rootDir must exist
// for the duration of the test. With readOnly, every remote write is
// rejected (PermissionDenied), which exercises upload/download failure
// paths. Enabling SFTP replaces the shell behaviour: the terminal tests
// (which do not enable it) are unaffected.
func (s *Server) EnableSFTP(rootDir string, readOnly bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sftpRoot = rootDir
	s.sftpReadOnly = readOnly
}

func (s *Server) acceptLoop() {
	for {
		conn, err := s.ln.Accept()
		if err != nil {
			return
		}
		s.mu.Lock()
		stall := s.StallHandshake
		s.mu.Unlock()
		if stall {
			// Leave the connection open without responding; the client's own
			// timeout/cancel must cut it.
			go io.Copy(io.Discard, conn)
			continue
		}
		go s.handleConn(conn)
	}
}

func (s *Server) handleConn(conn net.Conn) {
	sconn, chans, reqs, err := ssh.NewServerConn(conn, s.config)
	if err != nil {
		_ = conn.Close()
		return
	}
	go ssh.DiscardRequests(reqs)
	s.mu.Lock()
	stall := s.StallSessionOpen
	s.mu.Unlock()
	if stall {
		// Drain channel-opens without replying: the client's NewSession hangs
		// until it aborts, and the mux closes chans when the connection drops,
		// so this goroutine exits with no leak.
		for range chans {
		}
		_ = sconn.Close()
		return
	}
	for newCh := range chans {
		if newCh.ChannelType() != "session" {
			_ = newCh.Reject(ssh.UnknownChannelType, "unsupported channel type")
			continue
		}
		// Re-read per channel so SetSessionOpenDelay can be called while the
		// connection is already established (e.g. after the terminal session).
		s.mu.Lock()
		delay := s.sessionOpenDelay
		s.mu.Unlock()
		if delay > 0 {
			// Hold the channel-open answer back so the client's NewSession
			// resolves only after the delay — a client that already gave up
			// gets a late confirmation it must close.
			time.Sleep(delay)
		}
		ch, reqs, err := newCh.Accept()
		if err != nil {
			continue
		}
		s.totalSessionsOpened.Add(1)
		go s.serveSession(ch, reqs)
	}
	_ = sconn.Close()
}

// serveSession dispatches the session handler: the test hook when set,
// otherwise the default echo+exit handler. When SFTP is enabled, session
// channels answer the "sftp" subsystem with a real pkg/sftp server.
func (s *Server) serveSession(ch ssh.Channel, reqs <-chan *ssh.Request) {
	s.activeSessions.Add(1)
	defer s.activeSessions.Add(-1)
	s.mu.Lock()
	onShell := s.OnShell
	onExec := s.OnExec
	stallPTY := s.StallPTY
	sftpRoot := s.sftpRoot
	sftpReadOnly := s.sftpReadOnly
	s.mu.Unlock()
	if stallPTY {
		// Never read requests, so the pty-req stays unanswered. Reading the
		// channel drains in-band data until the client aborts the connection,
		// then returns with no leak.
		_, _ = io.Copy(io.Discard, ch)
		_ = ch.Close()
		return
	}
	if sftpRoot != "" {
		s.serveSFTP(ch, reqs, sftpRoot, sftpReadOnly)
		return
	}
	if onExec != nil {
		dispatchExec(ch, reqs, onShell, onExec)
		return
	}
	if onShell != nil {
		onShell(ch, reqs)
		return
	}
	DefaultShell(ch, reqs)
}

// dispatchExec routes a session channel by its first request: an "exec"
// request runs the exec handler with the parsed command; anything else is a
// shell session and goes to the shell handler (or the default echo shell)
// with every request preserved.
func dispatchExec(ch ssh.Channel, reqs <-chan *ssh.Request, onShell func(ch ssh.Channel, reqs <-chan *ssh.Request), onExec func(ch ssh.Channel, reqs <-chan *ssh.Request, command string)) {
	first, ok := <-reqs
	if !ok {
		_ = ch.Close()
		return
	}
	if first.Type == "exec" {
		command, ok := parseExecCommand(first.Payload)
		_ = first.Reply(ok, nil)
		if !ok {
			_ = ch.Close()
			return
		}
		onExec(ch, reqs, command)
		return
	}
	all := prepend(first, reqs)
	if onShell != nil {
		onShell(ch, all)
		return
	}
	DefaultShell(ch, all)
}

// parseExecCommand decodes an SSH exec request payload into the command.
func parseExecCommand(payload []byte) (string, bool) {
	command, rest, ok := ParseString(payload)
	return command, ok && len(rest) == 0
}

// prepend yields first followed by everything from rest, preserving request
// order for a dispatcher that already consumed the first request.
func prepend(first *ssh.Request, rest <-chan *ssh.Request) <-chan *ssh.Request {
	all := make(chan *ssh.Request, 1)
	all <- first
	go func() {
		defer close(all)
		for req := range rest {
			all <- req
		}
	}()
	return all
}

// serveSFTP serves one session channel: an "sftp" subsystem request runs a
// real pkg/sftp server over the channel; any other first request treats the
// channel as a shell (pty-req/shell/exec accepted, stdin echoed, exit 0), so
// an SFTP-enabled server still supports the terminal sessions sshclient.Connect
// opens alongside the SFTP channel. The first request decides the role — a
// client sends pty-req before shell, or the subsystem before any data — so
// the sftp server is never raced by the echo loop.
func (s *Server) serveSFTP(ch ssh.Channel, reqs <-chan *ssh.Request, root string, readOnly bool) {
	first, ok := <-reqs
	if !ok {
		_ = ch.Close()
		return
	}
	if first.Type == "subsystem" && isSFTPSubsystem(first.Payload) {
		_ = first.Reply(true, nil)
		opts := []sftp.ServerOption{sftp.WithServerWorkingDirectory(root)}
		if readOnly {
			opts = append(opts, sftp.ReadOnly())
		}
		srv, err := sftp.NewServer(ch, opts...)
		if err == nil {
			_ = srv.Serve()
		}
		_ = ch.Close()
		return
	}
	_ = first.Reply(shellRequest(first.Type), nil)
	go func() {
		for req := range reqs {
			_ = req.Reply(shellRequest(req.Type), nil)
		}
	}()
	_, _ = io.Copy(ch, ch)
	_, _ = ch.SendRequest("exit-status", false, ssh.Marshal(struct{ Status uint32 }{0}))
	_ = ch.Close()
}

// shellRequest reports whether a session request belongs to a terminal
// session.
func shellRequest(t string) bool {
	return t == "pty-req" || t == "shell" || t == "exec"
}

// isSFTPSubsystem reports whether a subsystem request payload names "sftp"
// (an SSH string: uint32 length + bytes).
func isSFTPSubsystem(payload []byte) bool {
	name, rest, ok := ParseString(payload)
	return ok && len(rest) == 0 && name == "sftp"
}

// DefaultShell replies ok to pty-req/shell/exec and echoes stdin to stdout,
// then exits 0.
func DefaultShell(ch ssh.Channel, reqs <-chan *ssh.Request) {
	go func() {
		for req := range reqs {
			switch req.Type {
			case "pty-req":
				_ = req.Reply(true, nil)
			case "shell", "exec":
				_ = req.Reply(true, nil)
			default:
				_ = req.Reply(false, nil)
			}
		}
	}()
	_, _ = io.Copy(ch, ch)
	_, _ = ch.SendRequest("exit-status", false, ssh.Marshal(struct{ Status uint32 }{0}))
	_ = ch.Close()
}

// SizeShell is a shell handler for PTY/resize tests: it records every
// pty-req and window-change size as (rows, cols) pairs into sizes, echoes
// stdin to stdout, then closes exited and exits 0. Both requests carry the
// size as width(cols) then height(rows) per RFC 4254 §6.2/§6.7.
func SizeShell(sizes chan<- [2]int, exited chan<- struct{}) func(ch ssh.Channel, reqs <-chan *ssh.Request) {
	return func(ch ssh.Channel, reqs <-chan *ssh.Request) {
		var rows, cols int
		send := func() {
			select {
			case sizes <- [2]int{rows, cols}:
			default:
			}
		}
		go func() {
			for req := range reqs {
				switch req.Type {
				case "pty-req":
					_, rest, ok := ParseString(req.Payload)
					if ok && len(rest) >= 8 {
						cols = int(binary.BigEndian.Uint32(rest[0:4]))
						rows = int(binary.BigEndian.Uint32(rest[4:8]))
						send()
					}
					_ = req.Reply(true, nil)
				case "shell", "exec":
					_ = req.Reply(true, nil)
				case "window-change":
					if len(req.Payload) >= 8 {
						cols = int(binary.BigEndian.Uint32(req.Payload[0:4]))
						rows = int(binary.BigEndian.Uint32(req.Payload[4:8]))
						send()
					}
					_ = req.Reply(true, nil)
				default:
					_ = req.Reply(false, nil)
				}
			}
		}()
		_, _ = io.Copy(ch, ch)
		close(exited)
		_, _ = ch.SendRequest("exit-status", false, ssh.Marshal(struct{ Status uint32 }{0}))
		_ = ch.Close()
	}
}

// TermShell is a PTY handler for terminal-type tests: it records the pty-req
// terminal string into terms, echoes stdin to stdout, then closes exited and
// exits 0.
func TermShell(terms chan<- string, exited chan<- struct{}) func(ch ssh.Channel, reqs <-chan *ssh.Request) {
	return func(ch ssh.Channel, reqs <-chan *ssh.Request) {
		go func() {
			for req := range reqs {
				switch req.Type {
				case "pty-req":
					term, _, ok := ParseString(req.Payload)
					if ok {
						terms <- term
					}
					_ = req.Reply(true, nil)
				case "shell", "exec":
					_ = req.Reply(true, nil)
				default:
					_ = req.Reply(false, nil)
				}
			}
		}()
		_, _ = io.Copy(ch, ch)
		close(exited)
		_, _ = ch.SendRequest("exit-status", false, ssh.Marshal(struct{ Status uint32 }{0}))
		_ = ch.Close()
	}
}

// EchoExec is an exec handler that writes the command back to stdout and
// exits 0 — a controllable echo for stdout/parity tests.
func EchoExec(ch ssh.Channel, reqs <-chan *ssh.Request, command string) {
	go drainRequests(reqs)
	_, _ = ch.Write([]byte(command))
	_, _ = ch.SendRequest("exit-status", false, ssh.Marshal(struct{ Status uint32 }{0}))
	_ = ch.Close()
}

// ExitExec is an exec handler that writes stdout and stderr (empty strings
// skip the stream), then exits with status. Large payloads exercise the
// client's output caps.
func ExitExec(stdout, stderr string, status uint32) func(ch ssh.Channel, reqs <-chan *ssh.Request, command string) {
	return func(ch ssh.Channel, reqs <-chan *ssh.Request, command string) {
		go drainRequests(reqs)
		if stderr != "" {
			_, _ = ch.Stderr().Write([]byte(stderr))
		}
		if stdout != "" {
			_, _ = ch.Write([]byte(stdout))
		}
		_, _ = ch.SendRequest("exit-status", false, ssh.Marshal(struct{ Status uint32 }{status}))
		_ = ch.Close()
	}
}

// StallExec is an exec handler that replies to the exec request but never
// writes or exits, so the client's Run hangs until its own timeout/cancel
// closes the channel. It blocks on the request stream (not the data stream,
// which EOFs as soon as the client's empty stdin closes) until the client
// closes the session.
func StallExec(ch ssh.Channel, reqs <-chan *ssh.Request, command string) {
	for range reqs {
	}
	_ = ch.Close()
}

// drainRequests replies false to every remaining session request until the
// channel closes, so an exec handler that ignores stdin never blocks the
// request stream.
func drainRequests(reqs <-chan *ssh.Request) {
	for req := range reqs {
		_ = req.Reply(false, nil)
	}
}

// ParseString decodes one SSH string (uint32 length + bytes) from b.
func ParseString(b []byte) (string, []byte, bool) {
	if len(b) < 4 {
		return "", nil, false
	}
	n := binary.BigEndian.Uint32(b[0:4])
	if uint32(len(b)-4) < n {
		return "", nil, false
	}
	return string(b[4 : 4+n]), b[4+n:], true
}

func errAuth(method string) error {
	return &authError{method}
}

type authError struct{ method string }

func (e *authError) Error() string { return "ssh: auth failed: " + e.method }

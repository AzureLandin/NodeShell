// Package sessions owns the interactive SSH session map: connect (with
// host-key trust and credential resolution), per-session write/resize/
// disconnect, cancel-all in-flight connects, output batching, and the
// session:data/closed/error Wails events. It depends on the sshclient and
// knownhosts packages, never on Wails itself.
package sessions

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"

	"golang.org/x/crypto/ssh"

	"nodeshell/internal/apperror"
	"nodeshell/internal/credentials"
	"nodeshell/internal/hosts"
	"nodeshell/internal/sshclient"
)

// Event names match src/shared/types.ts IPC constants.
const (
	EventSessionData   = "session:data"
	EventSessionClosed = "session:closed"
	EventSessionError  = "session:error"
)

// EventSink is the event emission seam: the production implementation emits
// through the Wails runtime, tests record into a slice. A nil sink is
// accepted and becomes a no-op.
type EventSink interface {
	Emit(event string, payload any)
}

// nopSink drops every event; the default for a Manager wired without a sink.
type nopSink struct{}

func (nopSink) Emit(string, any) {}

// DataEvent is the session:data payload (matches SessionDataEvent).
type DataEvent struct {
	SessionID string `json:"sessionId"`
	Data      string `json:"data"`
}

// ClosedEvent is the session:closed payload (matches SessionClosedEvent).
type ClosedEvent struct {
	SessionID string `json:"sessionId"`
}

// ErrorEvent is the session:error payload (matches SessionErrorEvent).
type ErrorEvent struct {
	SessionID string   `json:"sessionId"`
	Error     AppError `json:"error"`
}

// AppError mirrors src/shared/types.ts AppError.
type AppError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// ConnectResult is the sessions.connect return shape.
type ConnectResult struct {
	SessionID string `json:"sessionId"`
}

// ConnectOptions mirrors ConnectOptions in src/shared/types.ts.
type ConnectOptions struct {
	Password      string `json:"password"`
	AcceptHostKey bool   `json:"acceptHostKey"`
}

// Error carries the stable code for session-level failures.
type Error struct {
	Code    string
	Message string
}

func (e *Error) Error() string { return e.Message }

// ErrorCode lets apperror.Format carry the stable code across IPC.
func (e *Error) ErrorCode() string { return e.Code }

// Hosts is the narrow host-config dependency (hosts.Store satisfies it).
type Hosts interface {
	GetByID(id string) (hosts.HostConfig, bool, error)
}

// Credentials is the narrow secret dependency (credentials.Store satisfies
// it). The stale credentialsSaved flag never gates the lookup: only a real
// keyring hit counts.
type Credentials interface {
	Get(hostID string) (credentials.Secrets, bool, error)
}

// Conn is the narrow SSH-session dependency of the manager. The production
// implementation is *sshclient.Session; tests inject a fake over io.Pipe.
type Conn interface {
	Write(p []byte) (int, error)
	Resize(cols, rows int) error
	Wait() error
	Close() error
	Stdout() io.Reader
	Stderr() io.Reader
	Fingerprint() string
}

// Connector establishes one SSH session; production uses sshclient.Connect,
// tests inject a fake.
type Connector interface {
	Connect(ctx context.Context, opts sshclient.Options) (Conn, error)
}

// SFTPProvider is implemented by Conns that can open an SFTP client over
// their SSH connection (*sshclient.Session does). It is a separate interface
// so the terminal-path fakes in tests never need to know about SFTP.
type SFTPProvider interface {
	NewSFTPClient() (sshclient.SFTPClient, error)
}

// ExecProvider is implemented by Conns that can run a non-interactive remote
// command over their SSH connection (*sshclient.Session does). It is a
// separate interface so the terminal-path fakes in tests never need to know
// about exec.
type ExecProvider interface {
	Exec(ctx context.Context, command string, timeout time.Duration) (string, error)
}

// DialProvider is implemented by Conns that can open a TCP connection through
// the SSH session (*sshclient.Session does, via direct-tcpip). Terminal-path
// fakes never need to know about port forwarding.
type DialProvider interface {
	Dial(network, addr string) (net.Conn, error)
}

// ConnectorFunc adapts a plain function to the Connector interface.
type ConnectorFunc func(ctx context.Context, opts sshclient.Options) (Conn, error)

func (f ConnectorFunc) Connect(ctx context.Context, opts sshclient.Options) (Conn, error) {
	return f(ctx, opts)
}

// Manager tracks every interactive session and every in-flight connect.
type Manager struct {
	hosts    Hosts
	hostKeys sshclient.HostKeys
	creds    Credentials
	readKey  credentials.PrivateKeyReader
	connect  Connector
	sink     EventSink
	uuid     func() string

	mu       sync.Mutex
	sessions map[string]*Session
	// inflight maps a unique connect id to its cancel function; cancelConnect
	// cancels all of them (never a single pointer).
	inflight map[string]context.CancelFunc
	// closing is set by DisposeAll: it rejects late connects so a session can
	// never be inserted after shutdown.
	closing bool
}

// Deps wires a Manager. Connector defaults to sshclient.Connect; UUID to
// uuid.NewString.
type Deps struct {
	Hosts     Hosts
	HostKeys  sshclient.HostKeys
	Creds     Credentials
	ReadKey   credentials.PrivateKeyReader
	Sink      EventSink
	Connector Connector
	UUID      func() string
}

// New builds a Manager.
func New(d Deps) *Manager {
	if d.Connector == nil {
		d.Connector = ConnectorFunc(func(ctx context.Context, opts sshclient.Options) (Conn, error) {
			return sshclient.Connect(ctx, opts)
		})
	}
	if d.UUID == nil {
		d.UUID = uuid.NewString
	}
	if d.Sink == nil {
		d.Sink = nopSink{}
	}
	return &Manager{
		hosts:    d.Hosts,
		hostKeys: d.HostKeys,
		creds:    d.Creds,
		readKey:  d.ReadKey,
		connect:  d.Connector,
		sink:     d.Sink,
		uuid:     d.UUID,
		sessions: map[string]*Session{},
		inflight: map[string]context.CancelFunc{},
	}
}

// Connect establishes a session for the host, returning once the SSH
// connection, PTY and shell are fully usable. The connect is registered
// in-flight so CancelConnect can abort it; a session that has already
// completed is never affected.
func (m *Manager) Connect(ctx context.Context, hostID string, opts ConnectOptions) (ConnectResult, error) {
	host, ok, err := m.hostsByID(hostID)
	if err != nil {
		return ConnectResult{}, err
	}
	if !ok {
		return ConnectResult{}, &Error{Code: apperror.HostNotFound, Message: fmt.Sprintf("Host not found: %s", hostID)}
	}

	sshOpts := sshclient.Options{
		Host:           host.Host,
		Port:           host.Port,
		Username:       host.Username,
		AuthMethod:     host.AuthMethod,
		Password:       opts.Password,
		PrivateKeyPath: host.PrivateKeyPath,
		AcceptHostKey:  opts.AcceptHostKey,
		HostKeys:       m.hostKeys,
	}
	// Credentials: only an actual keyring hit is used; the persisted
	// credentialsSaved flag is ignored (a stale flag must never decide the
	// lookup). Options password wins over the stored one.
	if m.creds != nil {
		secrets, found, err := m.creds.Get(hostID)
		if err != nil {
			return ConnectResult{}, err
		}
		if found {
			if sshOpts.Password == "" {
				sshOpts.Password = secrets.Password
			}
			sshOpts.PrivateKey = secrets.PrivateKey
		}
	}
	if sshOpts.PrivateKey == "" && sshOpts.PrivateKeyPath != "" {
		sshOpts.KeyReader = m.readKey
	}

	ctx, cancel := context.WithCancel(ctx)
	connectID := m.uuid()
	m.mu.Lock()
	m.inflight[connectID] = cancel
	m.mu.Unlock()
	defer func() {
		m.mu.Lock()
		delete(m.inflight, connectID)
		m.mu.Unlock()
	}()

	conn, err := m.connect.Connect(ctx, sshOpts)
	if err != nil {
		return ConnectResult{}, err
	}

	// acceptHostKey is remembered only now that the SSH connection AND the
	// PTY shell succeeded — an auth failure never pollutes the trust store. A
	// failed remember is an observable connect failure: the accepted key was
	// not persisted, so the session must not be handed out.
	if opts.AcceptHostKey {
		if fp := conn.Fingerprint(); fp != "" {
			if err := m.hostKeys.Remember(host.Host, host.Port, fp); err != nil {
				_ = conn.Close()
				return ConnectResult{}, err
			}
		}
	}

	sess := &Session{
		manager: m,
		ID:      m.uuid(),
		hostID:  hostID,
		conn:    conn,
	}
	sess.batcher = newOutputBatcher(flushInterval, flushBytes, func(data []byte) {
		m.sink.Emit(EventSessionData, DataEvent{SessionID: sess.ID, Data: string(data)})
	})

	// Register the session and close the shutdown gate in one critical
	// section: a connect that finishes after DisposeAll must not insert a
	// session post-shutdown.
	m.mu.Lock()
	if m.closing {
		m.mu.Unlock()
		_ = conn.Close()
		return ConnectResult{}, &Error{Code: apperror.Cancelled, Message: "Connection cancelled"}
	}
	m.sessions[sess.ID] = sess
	m.mu.Unlock()

	// Output pumps: stdout and stderr both feed the same batcher (the
	// mutex serialises appends, so per-stream order is preserved).
	sess.pumps.Add(2)
	go m.pump(sess, conn.Stdout())
	go m.pump(sess, conn.Stderr())

	// Wait pump: a remote close or transport failure ends the session once.
	// Wait can return (exit-status received) while the tail of the stream is
	// still in flight, so the end path drains the pumps before it closes the
	// batcher.
	go func() {
		err := conn.Wait()
		sess.pumps.Wait() // never close the batcher ahead of the output pumps
		m.waitEnd(sess, err)
	}()

	return ConnectResult{SessionID: sess.ID}, nil
}

// hostsByID resolves the host config. A nil Hosts dependency (uninitialised
// manager) is an observable error, never fake success.
func (m *Manager) hostsByID(hostID string) (hosts.HostConfig, bool, error) {
	if m.hosts == nil {
		return hosts.HostConfig{}, false, errUnavailable
	}
	return m.hosts.GetByID(hostID)
}

// errUnavailable is returned when the manager was not wired with a hosts
// store (startup failure), so calls fail observably.
var errUnavailable = errors.New("nodeshell: sessions not initialised")

// CancelConnect aborts every in-flight connect; established sessions are
// unaffected.
func (m *Manager) CancelConnect() {
	m.mu.Lock()
	cancels := make([]context.CancelFunc, 0, len(m.inflight))
	for _, c := range m.inflight {
		cancels = append(cancels, c)
	}
	m.mu.Unlock()
	for _, c := range cancels {
		c()
	}
}

// Write sends data to the session's stdin. Same-session writes are
// serialised.
func (m *Manager) Write(sessionID string, data string) error {
	sess := m.get(sessionID)
	if sess == nil {
		return &Error{Code: apperror.SessionNotFound, Message: fmt.Sprintf("Session not found: %s", sessionID)}
	}
	return sess.Write(data)
}

// Resize sends an SSH window-change for the session.
func (m *Manager) Resize(sessionID string, cols, rows int) error {
	sess := m.get(sessionID)
	if sess == nil {
		return &Error{Code: apperror.SessionNotFound, Message: fmt.Sprintf("Session not found: %s", sessionID)}
	}
	return sess.Resize(cols, rows)
}

// Disconnect ends the session; unknown ids are a no-op success (mirrors the
// Electron backend).
func (m *Manager) Disconnect(sessionID string) error {
	if sess := m.get(sessionID); sess != nil {
		sess.end(nil)
	}
	return nil
}

// NewSFTPClient opens an SFTP client over the session's SSH connection. The
// returned client must be Closed by the caller; the SFTP service caches one
// per session and disposes it when the session ends.
func (m *Manager) NewSFTPClient(sessionID string) (sshclient.SFTPClient, error) {
	sess := m.get(sessionID)
	if sess == nil {
		return nil, &Error{Code: apperror.SessionNotFound, Message: fmt.Sprintf("Session not found: %s", sessionID)}
	}
	p, ok := sess.conn.(SFTPProvider)
	if !ok {
		return nil, &Error{Code: apperror.Unknown, Message: "SFTP is unavailable for this session"}
	}
	return p.NewSFTPClient()
}

// Exec runs a non-interactive command over the session's SSH connection —
// never the interactive PTY. The timeout bounds the whole command; ctx
// cancellation and timeout surface as coded errors.
func (m *Manager) Exec(sessionID string, ctx context.Context, command string, timeout time.Duration) (string, error) {
	sess := m.get(sessionID)
	if sess == nil {
		return "", &Error{Code: apperror.SessionNotFound, Message: fmt.Sprintf("Session not found: %s", sessionID)}
	}
	p, ok := sess.conn.(ExecProvider)
	if !ok {
		return "", &Error{Code: apperror.Unknown, Message: "Exec is unavailable for this session"}
	}
	return p.Exec(ctx, command, timeout)
}

// CanDial reports whether the session can open a direct-tcpip channel.
func (m *Manager) CanDial(sessionID string) error {
	sess := m.get(sessionID)
	if sess == nil {
		return &Error{Code: apperror.SessionNotFound, Message: fmt.Sprintf("Session not found: %s", sessionID)}
	}
	if _, ok := sess.conn.(DialProvider); !ok {
		return &Error{Code: apperror.Unknown, Message: "Port forwarding is unavailable for this session"}
	}
	return nil
}

// Dial opens a TCP connection through the session (SSH direct-tcpip).
func (m *Manager) Dial(sessionID, network, addr string) (net.Conn, error) {
	sess := m.get(sessionID)
	if sess == nil {
		return nil, &Error{Code: apperror.SessionNotFound, Message: fmt.Sprintf("Session not found: %s", sessionID)}
	}
	p, ok := sess.conn.(DialProvider)
	if !ok {
		return nil, &Error{Code: apperror.Unknown, Message: "Port forwarding is unavailable for this session"}
	}
	return p.Dial(network, addr)
}

// DisposeAll tears down every session and in-flight connect quietly (app
// shutdown); no events are emitted. The closing gate rejects any connect that
// finishes afterwards, so no session can be inserted after shutdown.
func (m *Manager) DisposeAll() {
	m.CancelConnect()
	m.mu.Lock()
	m.closing = true
	all := make([]*Session, 0, len(m.sessions))
	for _, s := range m.sessions {
		all = append(all, s)
	}
	m.mu.Unlock()
	for _, s := range all {
		s.disposeQuiet()
	}
}

func (m *Manager) get(sessionID string) *Session {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.sessions[sessionID]
}

// pump reads one stream into the batcher until EOF or error.
func (m *Manager) pump(sess *Session, r io.Reader) {
	defer sess.pumps.Done()
	buf := make([]byte, 32*1024)
	for {
		n, err := r.Read(buf)
		if n > 0 {
			sess.batcher.Add(buf[:n])
		}
		if err != nil {
			if err != io.EOF {
				sess.end(sess.classifyEndError(err))
			}
			return
		}
	}
}

// waitEnd finishes the session after the remote closes or the transport
// fails: a clean exit-status is a normal end (closed only), a transport
// fault surfaces as session:error followed by session:closed. The caller
// must have drained the output pumps first.
func (m *Manager) waitEnd(sess *Session, err error) {
	sess.end(sess.classifyEndError(err))
}

// classifyEndError maps a read/wait/write error onto the end state: nil for
// normal ends (remote exit, clean EOF, local teardown), the mapped error
// otherwise. Classification is type-based — a genuine remote fault such as a
// reset or broken pipe always surfaces, and only the side effects of a local
// Disconnect/Dispose close are silenced.
func (s *Session) classifyEndError(err error) error {
	if err == nil {
		return nil
	}
	var exitErr *ssh.ExitError
	if errors.As(err, &exitErr) {
		return nil
	}
	if errors.Is(err, io.EOF) {
		return nil
	}
	// Only a local teardown produces these: the session records the local
	// close state before it closes the connection, so the pumps' unblocking
	// errors are recognised as side effects, never as transport faults.
	if s.localClose.Load() || errors.Is(err, net.ErrClosed) {
		return nil
	}
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return &Error{Code: apperror.Timeout, Message: "Connection timed out"}
	}
	return &Error{Code: apperror.Unknown, Message: "Connection lost"}
}

// Session is one established interactive session handle.
type Session struct {
	manager *Manager
	ID      string
	hostID  string
	conn    Conn
	batcher *outputBatcher

	writeMu sync.Mutex
	// pumps counts the stdout/stderr pump goroutines; the end path waits for
	// them to drain before closing the batcher, so a stream tail that arrives
	// after Wait returned is never truncated.
	pumps sync.WaitGroup
	// localClose records that this session was torn down by the local side
	// (Disconnect/DisposeAll), so the resulting reader errors are silent.
	localClose atomic.Bool
	// firstFault is the first non-nil end error, recorded before endOnce so a
	// fault that a pump already surfaced can never be swallowed by a racing
	// clean end(nil) winning the once. CompareAndSwap keeps the first writer.
	firstFault atomic.Pointer[error]

	endOnce sync.Once
}

// recordFault stores the first non-nil end error; later nil/non-nil calls are
// no-ops. It must run before endOnce.Do so the finish path observes faults
// recorded by any racing caller.
func (s *Session) recordFault(err error) {
	if err == nil {
		return
	}
	e := err
	s.firstFault.CompareAndSwap(nil, &e)
}

// Write serialises per-session stdin writes.
func (s *Session) Write(data string) error {
	s.writeMu.Lock()
	_, err := s.conn.Write([]byte(data))
	s.writeMu.Unlock()
	if err != nil {
		if e := s.classifyEndError(err); e != nil {
			s.end(e)
		}
		return err
	}
	return nil
}

// Resize forwards an SSH window-change.
func (s *Session) Resize(cols, rows int) error {
	return s.conn.Resize(cols, rows)
}

// end tears the session down exactly once: mark the local close (so racing
// reader errors are silent), close the conn (unblocking the pumps), flush +
// unregister, then emit session:error (if any fault was recorded) followed by
// session:closed.
func (s *Session) end(err error) {
	s.recordFault(err)
	s.endOnce.Do(s.finish)
}

// finish is the endOnce body. It reads the fault slot — not the caller's err —
// so a fault recorded by a racing pump surfaces even when a clean end(nil)
// won the once.
func (s *Session) finish() {
	s.localClose.Store(true)
	_ = s.conn.Close()
	s.batcher.Close()
	m := s.manager
	m.mu.Lock()
	delete(m.sessions, s.ID)
	m.mu.Unlock()
	if err := s.firstFault.Load(); err != nil {
		m.sink.Emit(EventSessionError, ErrorEvent{SessionID: s.ID, Error: AppError{Code: errorCode(*err), Message: (*err).Error()}})
	}
	m.sink.Emit(EventSessionClosed, ClosedEvent{SessionID: s.ID})
}

// disposeQuiet closes everything without events (app shutdown path). The
// batcher is discarded before the conn closes, so the pumps' unblocked reads
// of the tail can never be flushed as a data event.
func (s *Session) disposeQuiet() {
	s.endOnce.Do(func() {
		s.localClose.Store(true)
		s.batcher.Discard()
		_ = s.conn.Close()
		s.manager.mu.Lock()
		delete(s.manager.sessions, s.ID)
		s.manager.mu.Unlock()
	})
}

// errorCode extracts a stable code from any error (typed Error or plain).
func errorCode(err error) string {
	var coded interface{ ErrorCode() string }
	if errors.As(err, &coded) {
		return coded.ErrorCode()
	}
	return apperror.Unknown
}

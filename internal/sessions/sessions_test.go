package sessions

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"nodeshell/internal/apperror"
	"nodeshell/internal/hosts"
	"nodeshell/internal/knownhosts"
	"nodeshell/internal/sshclient"
	"nodeshell/internal/sshtest"

	"golang.org/x/crypto/ssh"
)

// recordSink records every emitted event for assertions.
type recordSink struct {
	mu     sync.Mutex
	events []recordedEvent
}

type recordedEvent struct {
	name    string
	payload any
}

func (s *recordSink) Emit(name string, payload any) {
	s.mu.Lock()
	s.events = append(s.events, recordedEvent{name, payload})
	s.mu.Unlock()
}

func (s *recordSink) count(name string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for _, e := range s.events {
		if e.name == name {
			n++
		}
	}
	return n
}

func (s *recordSink) total() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.events)
}

// dataJoined concatenates every session:data payload in emission order.
func (s *recordSink) dataJoined() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	var sb strings.Builder
	for _, e := range s.events {
		if e.name == EventSessionData {
			if d, ok := e.payload.(DataEvent); ok {
				sb.WriteString(d.Data)
			}
		}
	}
	return sb.String()
}

// lastIndex returns the index of the last event named name, or -1.
func (s *recordSink) lastIndex(name string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := len(s.events) - 1; i >= 0; i-- {
		if s.events[i].name == name {
			return i
		}
	}
	return -1
}

// newTestManager wires a Manager over real hosts/knownhosts stores and the
// real sshclient connector (no mocks), with a recording sink.
func newTestManager(t *testing.T, dir string, sink *recordSink) (*Manager, *hosts.Store, *knownhosts.Store) {
	t.Helper()
	h := hosts.New(dir)
	k := knownhosts.New(dir)
	m := New(Deps{Hosts: h, HostKeys: k, Sink: sink})
	return m, h, k
}

// addHost registers a password host pointing at addr and returns its id.
func addHost(t *testing.T, h *hosts.Store, name, addr, user string) string {
	t.Helper()
	host, port := splitHostPort(t, addr)
	created, err := h.Create(hosts.HostInput{
		Name: name, Host: host, Port: port, Username: user, AuthMethod: "password",
	})
	if err != nil {
		t.Fatalf("create host: %v", err)
	}
	return created.Id
}

func splitHostPort(t *testing.T, addr string) (string, int) {
	t.Helper()
	idx := strings.LastIndex(addr, ":")
	if idx < 0 {
		t.Fatalf("bad addr %q", addr)
	}
	var port int
	for _, c := range addr[idx+1:] {
		port = port*10 + int(c-'0')
	}
	return addr[:idx], port
}

func assertErrorCode(t *testing.T, err error, code string) {
	t.Helper()
	if err == nil {
		t.Fatalf("error = nil, want code %s", code)
	}
	var coded interface{ ErrorCode() string }
	if !errors.As(err, &coded) {
		t.Fatalf("error = %v, want an error carrying code %s", err, code)
	}
	if got := coded.ErrorCode(); got != code {
		t.Fatalf("code = %s, want %s (error: %v)", got, code, err)
	}
}

// TestManagerConnectUnknownHost: a missing host id is HOST_NOT_FOUND and no
// connection is attempted.
func TestManagerConnectUnknownHost(t *testing.T) {
	sink := &recordSink{}
	m, _, _ := newTestManager(t, t.TempDir(), sink)
	_, err := m.Connect(context.Background(), "nope", ConnectOptions{Password: "x"})
	assertErrorCode(t, err, apperror.HostNotFound)
	if sink.total() != 0 {
		t.Fatalf("emitted %d events for a failed connect, want 0", sink.total())
	}
}

// TestManagerConnectDataAndClosedEvents drives the full real-server path:
// connect resolves once the PTY+shell is up, echo comes back as session:data,
// and disconnect ends with exactly one session:closed.
func TestManagerConnectDataAndClosedEvents(t *testing.T) {
	srv := sshtest.New(t)
	srv.PasswordOK = func(user, pass string) bool { return user == "user" && pass == "secret" }
	sink := &recordSink{}
	m, h, k := newTestManager(t, t.TempDir(), sink)
	hostID := addHost(t, h, "lab", srv.Addr, "user")
	host, port := splitHostPort(t, srv.Addr)
	if err := k.Remember(host, port, srv.HostKeyFingerprint()); err != nil {
		t.Fatalf("Remember: %v", err)
	}

	res, err := m.Connect(context.Background(), hostID, ConnectOptions{Password: "secret"})
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	if res.SessionID == "" {
		t.Fatal("Connect must return a session id")
	}
	if err := m.Write(res.SessionID, "hi\n"); err != nil {
		t.Fatalf("Write: %v", err)
	}
	waitFor(t, 3*time.Second, "echo data", func() bool {
		return strings.Contains(sink.dataJoined(), "hi\n")
	})
	if got := sink.count(EventSessionData); got == 0 {
		t.Fatal("no session:data events were emitted")
	}
	if err := m.Disconnect(res.SessionID); err != nil {
		t.Fatalf("Disconnect: %v", err)
	}
	waitFor(t, 3*time.Second, "closed event", func() bool { return sink.count(EventSessionClosed) == 1 })
	if got := sink.count(EventSessionClosed); got != 1 {
		t.Fatalf("session:closed emitted %d times, want exactly 1", got)
	}
	if got := sink.count(EventSessionError); got != 0 {
		t.Fatalf("session:error emitted %d times on clean disconnect, want 0", got)
	}
}

// TestManagerDisconnectUnknownIsNoOp mirrors the Electron backend.
func TestManagerDisconnectUnknownIsNoOp(t *testing.T) {
	sink := &recordSink{}
	m, _, _ := newTestManager(t, t.TempDir(), sink)
	if err := m.Disconnect("does-not-exist"); err != nil {
		t.Fatalf("Disconnect of unknown session: %v", err)
	}
}

// TestManagerWriteAfterDisconnectIsSessionNotFound: writes to a gone session
// fail observably.
func TestManagerWriteAfterDisconnectIsSessionNotFound(t *testing.T) {
	srv := sshtest.New(t)
	srv.PasswordOK = func(user, pass string) bool { return true }
	sink := &recordSink{}
	m, h, k := newTestManager(t, t.TempDir(), sink)
	hostID := addHost(t, h, "lab", srv.Addr, "user")
	host, port := splitHostPort(t, srv.Addr)
	if err := k.Remember(host, port, srv.HostKeyFingerprint()); err != nil {
		t.Fatalf("Remember: %v", err)
	}
	res, err := m.Connect(context.Background(), hostID, ConnectOptions{Password: "secret"})
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	if err := m.Disconnect(res.SessionID); err != nil {
		t.Fatalf("Disconnect: %v", err)
	}
	waitFor(t, 3*time.Second, "session removed", func() bool {
		return sink.count(EventSessionClosed) == 1
	})
	err = m.Write(res.SessionID, "x")
	assertErrorCode(t, err, apperror.SessionNotFound)
}

// TestManagerCancelConnectAllInflight: CancelConnect aborts every in-flight
// connect while leaving established sessions fully usable.
func TestManagerCancelConnectAllInflight(t *testing.T) {
	srv := sshtest.New(t)
	srv.PasswordOK = func(user, pass string) bool { return true }
	stall := sshtest.New(t)
	stall.StallHandshake = true

	sink := &recordSink{}
	m, h, k := newTestManager(t, t.TempDir(), sink)
	hostID := addHost(t, h, "lab", srv.Addr, "user")
	stallID := addHost(t, h, "stall", stall.Addr, "user")
	host, port := splitHostPort(t, srv.Addr)
	if err := k.Remember(host, port, srv.HostKeyFingerprint()); err != nil {
		t.Fatalf("Remember: %v", err)
	}

	// One established session that must survive the cancel.
	established, err := m.Connect(context.Background(), hostID, ConnectOptions{Password: "secret"})
	if err != nil {
		t.Fatalf("Connect established: %v", err)
	}

	// Two in-flight connects to the stall server.
	const n = 2
	var wg sync.WaitGroup
	errs := make([]error, n)
	results := make([]ConnectResult, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			results[i], errs[i] = m.Connect(context.Background(), stallID, ConnectOptions{Password: "x"})
		}(i)
	}
	time.Sleep(150 * time.Millisecond) // let both TCP dials land and register in-flight
	m.CancelConnect()
	wg.Wait()
	for i := 0; i < n; i++ {
		assertErrorCode(t, errs[i], apperror.Cancelled)
	}
	// The established session still works after the cancel.
	if err := m.Write(established.SessionID, "hi\n"); err != nil {
		t.Fatalf("Write after cancel: %v", err)
	}
	waitFor(t, 3*time.Second, "echo after cancel", func() bool {
		return strings.Contains(sink.dataJoined(), "hi\n")
	})
}

// TestManagerHostKeyTrustTiming: acceptHostKey is remembered only after the
// whole connect (auth + PTY + shell) succeeds; a failed auth or a refused
// unknown key never pollutes the trust store.
func TestManagerHostKeyTrustTiming(t *testing.T) {
	srv := sshtest.New(t)
	srv.PasswordOK = func(user, pass string) bool { return pass == "secret" }
	sink := &recordSink{}
	m, h, k := newTestManager(t, t.TempDir(), sink)
	hostID := addHost(t, h, "lab", srv.Addr, "user")
	host, port := splitHostPort(t, srv.Addr)
	check := func(status string) string {
		res, err := k.Check(host, port, srv.HostKeyFingerprint())
		if err != nil {
			t.Fatalf("Check: %v", err)
		}
		return res.Status
	}

	// 1. Unknown key, not accepted: HOST_KEY_UNKNOWN, nothing remembered.
	_, err := m.Connect(context.Background(), hostID, ConnectOptions{Password: "secret"})
	assertErrorCode(t, err, apperror.HostKeyUnknown)
	if got := check("x"); got != "unknown" {
		t.Fatalf("store status after refused unknown key = %q, want unknown", got)
	}

	// 2. acceptHostKey with a WRONG password: auth fails and the key is NOT
	// remembered — an auth failure must never pollute the trust store.
	_, err = m.Connect(context.Background(), hostID, ConnectOptions{Password: "wrong", AcceptHostKey: true})
	assertErrorCode(t, err, apperror.AuthFailed)
	if got := check("x"); got != "unknown" {
		t.Fatalf("store status after failed auth = %q, want unknown (trust must follow success)", got)
	}

	// 3. acceptHostKey with the right password: remembered.
	if _, err := m.Connect(context.Background(), hostID, ConnectOptions{Password: "secret", AcceptHostKey: true}); err != nil {
		t.Fatalf("Connect with accept: %v", err)
	}
	if got := check("x"); got != "ok" {
		t.Fatalf("store status after successful accepted connect = %q, want ok", got)
	}
}

// TestManagerClosedOnce: a remote close racing a local Disconnect emits
// session:closed exactly once.
func TestManagerClosedOnce(t *testing.T) {
	srv := sshtest.New(t)
	srv.PasswordOK = func(user, pass string) bool { return true }
	// Server closes the shell immediately once the shell request lands.
	srv.OnShell = func(ch ssh.Channel, reqs <-chan *ssh.Request) {
		shellSeen := make(chan struct{})
		go func() {
			defer close(shellSeen)
			for req := range reqs {
				_ = req.Reply(req.Type == "pty-req" || req.Type == "shell" || req.Type == "exec", nil)
				if req.Type == "shell" {
					return
				}
			}
		}()
		<-shellSeen
		_, _ = ch.SendRequest("exit-status", false, ssh.Marshal(struct{ Status uint32 }{0}))
		_ = ch.Close()
	}

	sink := &recordSink{}
	m, h, k := newTestManager(t, t.TempDir(), sink)
	hostID := addHost(t, h, "lab", srv.Addr, "user")
	host, port := splitHostPort(t, srv.Addr)
	if err := k.Remember(host, port, srv.HostKeyFingerprint()); err != nil {
		t.Fatalf("Remember: %v", err)
	}

	res, err := m.Connect(context.Background(), hostID, ConnectOptions{Password: "secret"})
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	// Race a local Disconnect against the remote close.
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); _ = m.Disconnect(res.SessionID) }()
	go func() { defer wg.Done(); time.Sleep(20 * time.Millisecond); _ = m.Disconnect(res.SessionID) }()
	wg.Wait()
	waitFor(t, 3*time.Second, "closed event", func() bool { return sink.count(EventSessionClosed) >= 1 })
	if got := sink.count(EventSessionClosed); got != 1 {
		t.Fatalf("session:closed emitted %d times, want exactly 1", got)
	}
}

// TestManagerResizeForwardsWindowChange through the manager.
func TestManagerResizeForwardsWindowChange(t *testing.T) {
	srv := sshtest.New(t)
	srv.PasswordOK = func(user, pass string) bool { return true }
	sizes := make(chan [2]int, 8)
	exited := make(chan struct{})
	srv.OnShell = sshtest.SizeShell(sizes, exited)

	sink := &recordSink{}
	m, h, k := newTestManager(t, t.TempDir(), sink)
	hostID := addHost(t, h, "lab", srv.Addr, "user")
	host, port := splitHostPort(t, srv.Addr)
	if err := k.Remember(host, port, srv.HostKeyFingerprint()); err != nil {
		t.Fatalf("Remember: %v", err)
	}
	res, err := m.Connect(context.Background(), hostID, ConnectOptions{Password: "secret"})
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	first := <-sizes
	if first != [2]int{24, 80} {
		t.Fatalf("initial pty size = %v, want [24 80]", first)
	}
	if err := m.Resize(res.SessionID, 132, 43); err != nil {
		t.Fatalf("Resize: %v", err)
	}
	second := <-sizes
	if second != [2]int{43, 132} {
		t.Fatalf("resized size = %v, want [43 132]", second)
	}
	if err := m.Resize("gone", 80, 24); err == nil {
		t.Fatal("Resize of unknown session must error")
	}
}

// TestManagerUTF8AcrossReads: a rune split across SSH reads reaches the
// frontend intact (never U+FFFD) through the manager's batcher.
func TestManagerUTF8AcrossReads(t *testing.T) {
	srv := sshtest.New(t)
	srv.PasswordOK = func(user, pass string) bool { return true }
	srv.OnShell = func(ch ssh.Channel, reqs <-chan *ssh.Request) {
		go func() {
			for req := range reqs {
				_ = req.Reply(req.Type == "pty-req" || req.Type == "shell" || req.Type == "exec", nil)
			}
		}()
		// 你 = E4 BD A0, split across three writes with delays.
		_, _ = ch.Write([]byte("\xE4"))
		time.Sleep(30 * time.Millisecond)
		_, _ = ch.Write([]byte("\xBD"))
		time.Sleep(30 * time.Millisecond)
		_, _ = ch.Write([]byte("\xA0ok"))
		time.Sleep(100 * time.Millisecond)
		_, _ = ch.SendRequest("exit-status", false, ssh.Marshal(struct{ Status uint32 }{0}))
		_ = ch.Close()
	}

	sink := &recordSink{}
	m, h, k := newTestManager(t, t.TempDir(), sink)
	hostID := addHost(t, h, "lab", srv.Addr, "user")
	host, port := splitHostPort(t, srv.Addr)
	if err := k.Remember(host, port, srv.HostKeyFingerprint()); err != nil {
		t.Fatalf("Remember: %v", err)
	}
	res, err := m.Connect(context.Background(), hostID, ConnectOptions{Password: "secret"})
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	waitFor(t, 3*time.Second, "utf8 data", func() bool {
		return strings.Contains(sink.dataJoined(), "ok")
	})
	joined := sink.dataJoined()
	if !strings.Contains(joined, "你") {
		t.Fatalf("data %q must contain 你 intact", joined)
	}
	if strings.Contains(joined, "\uFFFD") {
		t.Fatalf("data %q must not contain U+FFFD", joined)
	}
	if err := m.Disconnect(res.SessionID); err != nil {
		t.Fatalf("Disconnect: %v", err)
	}
}

// TestManagerTwoSessionsIndependent: each connect gets a unique id, and
// closing one session never affects the other.
func TestManagerTwoSessionsIndependent(t *testing.T) {
	srv := sshtest.New(t)
	srv.PasswordOK = func(user, pass string) bool { return true }
	sink := &recordSink{}
	m, h, k := newTestManager(t, t.TempDir(), sink)
	hostID := addHost(t, h, "lab", srv.Addr, "user")
	host, port := splitHostPort(t, srv.Addr)
	if err := k.Remember(host, port, srv.HostKeyFingerprint()); err != nil {
		t.Fatalf("Remember: %v", err)
	}
	a, err := m.Connect(context.Background(), hostID, ConnectOptions{Password: "secret"})
	if err != nil {
		t.Fatalf("Connect a: %v", err)
	}
	b, err := m.Connect(context.Background(), hostID, ConnectOptions{Password: "secret"})
	if err != nil {
		t.Fatalf("Connect b: %v", err)
	}
	if a.SessionID == b.SessionID {
		t.Fatalf("two connects share session id %q", a.SessionID)
	}
	if err := m.Disconnect(a.SessionID); err != nil {
		t.Fatalf("Disconnect a: %v", err)
	}
	// b still works.
	if err := m.Write(b.SessionID, "hi\n"); err != nil {
		t.Fatalf("Write b: %v", err)
	}
	waitFor(t, 3*time.Second, "echo from b", func() bool {
		return strings.Contains(sink.dataJoined(), "hi\n")
	})
}

// TestManagerDisposeAllQuiet: app shutdown disposes everything with no events
// and cancels in-flight connects.
func TestManagerDisposeAllQuiet(t *testing.T) {
	srv := sshtest.New(t)
	srv.PasswordOK = func(user, pass string) bool { return true }
	stall := sshtest.New(t)
	stall.StallHandshake = true

	sink := &recordSink{}
	m, h, k := newTestManager(t, t.TempDir(), sink)
	hostID := addHost(t, h, "lab", srv.Addr, "user")
	stallID := addHost(t, h, "stall", stall.Addr, "user")
	host, port := splitHostPort(t, srv.Addr)
	if err := k.Remember(host, port, srv.HostKeyFingerprint()); err != nil {
		t.Fatalf("Remember: %v", err)
	}
	established, err := m.Connect(context.Background(), hostID, ConnectOptions{Password: "secret"})
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	inflightDone := make(chan error, 1)
	go func() {
		_, err := m.Connect(context.Background(), stallID, ConnectOptions{Password: "x"})
		inflightDone <- err
	}()
	time.Sleep(150 * time.Millisecond)

	m.DisposeAll()

	// The in-flight connect was cancelled.
	select {
	case err := <-inflightDone:
		assertErrorCode(t, err, apperror.Cancelled)
	case <-time.After(5 * time.Second):
		t.Fatal("in-flight connect did not return after DisposeAll")
	}
	// The established session is gone without any event.
	if err := m.Write(established.SessionID, "x"); err == nil {
		t.Fatal("Write after DisposeAll must error")
	}
	if got := sink.total(); got != 0 {
		t.Fatalf("DisposeAll emitted %d events, want 0 (quiet teardown)", got)
	}
}

// RED/GREEN: quiet teardown must not flush pending output. A stream tail that
// is still buffered when DisposeAll runs is dropped, never emitted as
// session:data.
func TestManagerDisposeAllDropsPendingData(t *testing.T) {
	sink := &recordSink{}
	h := hosts.New(t.TempDir())
	hostID := addHost(t, h, "lab", "127.0.0.1:22", "user")
	conn := newGateTailConn("pending-tail")
	m := New(Deps{
		Hosts: h,
		Sink:  sink,
		Connector: ConnectorFunc(func(ctx context.Context, opts sshclient.Options) (Conn, error) {
			return conn, nil
		}),
	})
	if _, err := m.Connect(context.Background(), hostID, ConnectOptions{Password: "x"}); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	m.DisposeAll()
	// The pumps read the released tail and try to Add it; give every batcher
	// timer window a chance to flush a stray chunk before asserting.
	time.Sleep(150 * time.Millisecond)
	if got := sink.total(); got != 0 {
		t.Fatalf("DisposeAll emitted %d events with pending output, want 0 (quiet teardown must discard)", got)
	}
}

// failingHostKeys is a HostKeys store whose Remember always fails with the
// coded CONFIG_WRITE_FAILED error the production knownhosts.Store returns.
type failingHostKeys struct{}

func (failingHostKeys) Check(string, int, string) (knownhosts.CheckResult, error) {
	return knownhosts.CheckResult{Status: "ok"}, nil
}
func (failingHostKeys) Remember(string, int, string) error {
	return &knownhosts.Error{Code: apperror.ConfigWriteFailed, Message: "store write failed"}
}

// fpConn is a minimal Conn exposing a fingerprint, used to drive the
// post-connect Remember step.
type fpConn struct {
	closed chan struct{}
	once   sync.Once
}

func (c *fpConn) Write([]byte) (int, error) { return 0, nil }
func (c *fpConn) Resize(int, int) error     { return nil }
func (c *fpConn) Wait() error               { <-c.closed; return nil }
func (c *fpConn) Close() error              { c.once.Do(func() { close(c.closed) }); return nil }
func (c *fpConn) Stdout() io.Reader         { return &gateReader{ch: c.closed} }
func (c *fpConn) Stderr() io.Reader         { return &gateReader{ch: c.closed} }
func (c *fpConn) Fingerprint() string       { return "fp" }

// TestManagerRememberFailureFailsConnect: when accepting an unknown host key,
// a failed Remember must fail the connect observably and close the conn, not
// log-and-continue into an untrusted session. The CONFIG_WRITE_FAILED code
// from the store survives unchanged (apperror keeps it across IPC).
func TestManagerRememberFailureFailsConnect(t *testing.T) {
	sink := &recordSink{}
	h := hosts.New(t.TempDir())
	hostID := addHost(t, h, "lab", "127.0.0.1:22", "user")
	conn := &fpConn{closed: make(chan struct{})}
	m := New(Deps{
		Hosts:    h,
		HostKeys: failingHostKeys{},
		Sink:     sink,
		Connector: ConnectorFunc(func(ctx context.Context, opts sshclient.Options) (Conn, error) {
			return conn, nil
		}),
	})
	_, err := m.Connect(context.Background(), hostID, ConnectOptions{Password: "x", AcceptHostKey: true})
	assertErrorCode(t, err, apperror.ConfigWriteFailed)
	select {
	case <-conn.closed:
	default:
		t.Fatal("conn must be closed when Remember fails")
	}
	// No session may exist: the failed connect must not leave a session behind.
	m.mu.Lock()
	left := len(m.sessions)
	m.mu.Unlock()
	if left != 0 {
		t.Fatalf("%d sessions left behind by a failed Remember, want 0", left)
	}
	if sink.total() != 0 {
		t.Fatalf("emitted %d events for a failed connect, want 0", sink.total())
	}
}

// TestManagerNilSinkIsNoOp: a manager wired without a sink must not panic on
// connect, disconnect or dispose.
func TestManagerNilSinkIsNoOp(t *testing.T) {
	h := hosts.New(t.TempDir())
	hostID := addHost(t, h, "lab", "127.0.0.1:22", "user")
	conn := newGateTailConn("")
	m := New(Deps{
		Hosts: h,
		Connector: ConnectorFunc(func(ctx context.Context, opts sshclient.Options) (Conn, error) {
			return conn, nil
		}),
	})
	res, err := m.Connect(context.Background(), hostID, ConnectOptions{Password: "x"})
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	if err := m.Disconnect(res.SessionID); err != nil {
		t.Fatalf("Disconnect: %v", err)
	}
	if err := m.Write(res.SessionID, "x"); err == nil {
		t.Fatal("Write after disconnect must error")
	}
	m.DisposeAll()
}

// blockingConnector is a Connector seam that blocks until release, so the
// manager's in-flight connect can be observed mid-flight.
type blockingConnector struct {
	release chan struct{}
	conn    Conn
	started chan struct{}
	once    sync.Once
}

func (c *blockingConnector) Connect(ctx context.Context, opts sshclient.Options) (Conn, error) {
	c.once.Do(func() { close(c.started) })
	<-c.release
	return c.conn, nil
}

// TestManagerDisposeAllRejectsLateConnect: a connect that completes after
// DisposeAll must be rejected at the closing gate — the conn is closed and no
// session is inserted after shutdown.
func TestManagerDisposeAllRejectsLateConnect(t *testing.T) {
	sink := &recordSink{}
	h := hosts.New(t.TempDir())
	hostID := addHost(t, h, "lab", "127.0.0.1:22", "user")
	conn := &fpConn{closed: make(chan struct{})}
	blocked := &blockingConnector{release: make(chan struct{}), conn: conn, started: make(chan struct{})}
	m := New(Deps{
		Hosts: h,
		Sink:  sink,
		Connector: ConnectorFunc(func(ctx context.Context, opts sshclient.Options) (Conn, error) {
			return blocked.Connect(ctx, opts)
		}),
	})
	done := make(chan error, 1)
	go func() {
		_, err := m.Connect(context.Background(), hostID, ConnectOptions{Password: "x"})
		done <- err
	}()
	<-blocked.started
	m.DisposeAll()
	close(blocked.release) // the connect finishes after shutdown
	select {
	case err := <-done:
		assertErrorCode(t, err, apperror.Cancelled)
	case <-time.After(5 * time.Second):
		t.Fatal("late connect did not return after DisposeAll")
	}
	select {
	case <-conn.closed:
	default:
		t.Fatal("late connect's conn must be closed")
	}
	if got := sink.total(); got != 0 {
		t.Fatalf("late connect emitted %d events after DisposeAll, want 0", got)
	}
}

// TestManagerReconnectAfterDisconnect: a fresh connect after a session was
// torn down must work — no per-session state may leak into the next one.
func TestManagerReconnectAfterDisconnect(t *testing.T) {
	srv := sshtest.New(t)
	srv.PasswordOK = func(user, pass string) bool { return true }
	sink := &recordSink{}
	m, h, k := newTestManager(t, t.TempDir(), sink)
	hostID := addHost(t, h, "lab", srv.Addr, "user")
	host, port := splitHostPort(t, srv.Addr)
	if err := k.Remember(host, port, srv.HostKeyFingerprint()); err != nil {
		t.Fatalf("Remember: %v", err)
	}
	for i := 0; i < 2; i++ {
		res, err := m.Connect(context.Background(), hostID, ConnectOptions{Password: "secret"})
		if err != nil {
			t.Fatalf("connect %d: %v", i, err)
		}
		if err := m.Write(res.SessionID, "hi\n"); err != nil {
			t.Fatalf("write %d: %v", i, err)
		}
		waitFor(t, 3*time.Second, fmt.Sprintf("echo %d", i), func() bool {
			return strings.Count(sink.dataJoined(), "hi\n") >= i+1
		})
		if err := m.Disconnect(res.SessionID); err != nil {
			t.Fatalf("disconnect %d: %v", i, err)
		}
		waitFor(t, 3*time.Second, fmt.Sprintf("closed %d", i), func() bool {
			return sink.count(EventSessionClosed) >= i+1
		})
	}
}

// RED/GREEN: a real exec over the session's SSH connection returns the
// command's stdout without touching the interactive PTY.
func TestManagerExecReturnsOutput(t *testing.T) {
	srv := sshtest.New(t)
	srv.PasswordOK = func(user, pass string) bool { return true }
	srv.OnExec = sshtest.EchoExec
	sink := &recordSink{}
	m, h, k := newTestManager(t, t.TempDir(), sink)
	hostID := addHost(t, h, "lab", srv.Addr, "user")
	host, port := splitHostPort(t, srv.Addr)
	if err := k.Remember(host, port, srv.HostKeyFingerprint()); err != nil {
		t.Fatalf("Remember: %v", err)
	}
	res, err := m.Connect(context.Background(), hostID, ConnectOptions{Password: "secret"})
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	out, err := m.Exec(res.SessionID, context.Background(), "uname -a", 5*time.Second)
	if err != nil {
		t.Fatalf("Exec: %v", err)
	}
	if out != "uname -a" {
		t.Fatalf("stdout = %q, want %q", out, "uname -a")
	}
	// The interactive session still echoes input.
	if err := m.Write(res.SessionID, "hi\n"); err != nil {
		t.Fatalf("Write after exec: %v", err)
	}
	waitFor(t, 3*time.Second, "echo after exec", func() bool {
		return strings.Contains(sink.dataJoined(), "hi\n")
	})
}

// RED/GREEN: an unknown session id is SESSION_NOT_FOUND, aligned with
// NewSFTPClient.
func TestManagerExecUnknownSession(t *testing.T) {
	sink := &recordSink{}
	m, _, _ := newTestManager(t, t.TempDir(), sink)
	_, err := m.Exec("nope", context.Background(), "cmd", time.Second)
	assertErrorCode(t, err, apperror.SessionNotFound)
}

// RED/GREEN: a Conn that does not implement ExecProvider fails with coded
// UNKNOWN (the type assertion is not silently ignored).
func TestManagerExecNonProvider(t *testing.T) {
	sink := &recordSink{}
	h := hosts.New(t.TempDir())
	hostID := addHost(t, h, "lab", "127.0.0.1:22", "user")
	conn := newGateTailConn("")
	m := New(Deps{
		Hosts: h,
		Sink:  sink,
		Connector: ConnectorFunc(func(ctx context.Context, opts sshclient.Options) (Conn, error) {
			return conn, nil
		}),
	})
	res, err := m.Connect(context.Background(), hostID, ConnectOptions{Password: "x"})
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	_, err = m.Exec(res.SessionID, context.Background(), "cmd", time.Second)
	assertErrorCode(t, err, apperror.Unknown)
	_ = m.Disconnect(res.SessionID)
}

// --- write serialisation (controllable fake transport) ---

// trackingConn records the maximum number of overlapping Write calls; the
// manager must never run two stdin writes for the same session concurrently.
// done releases the stdout/stderr pumps at the end of the test.
type trackingConn struct {
	mu        sync.Mutex
	active    int
	maxActive int
	writes    [][]byte
	done      chan struct{}
}

func (c *trackingConn) Write(p []byte) (int, error) {
	c.mu.Lock()
	c.active++
	if c.active > c.maxActive {
		c.maxActive = c.active
	}
	c.mu.Unlock()
	time.Sleep(15 * time.Millisecond)
	c.mu.Lock()
	c.writes = append(c.writes, append([]byte(nil), p...))
	c.active--
	c.mu.Unlock()
	return len(p), nil
}

func (c *trackingConn) Resize(int, int) error { return nil }
func (c *trackingConn) Wait() error {
	<-c.done
	return nil
}
func (c *trackingConn) Close() error        { return nil }
func (c *trackingConn) Stdout() io.Reader   { return &gateReader{ch: c.done} }
func (c *trackingConn) Stderr() io.Reader   { return &gateReader{ch: c.done} }
func (c *trackingConn) Fingerprint() string { return "" }

// gateReader blocks until released, so the session stays alive during the
// test and the pump goroutine ends when we release it.
type gateReader struct{ ch chan struct{} }

func (g *gateReader) Read(p []byte) (int, error) {
	<-g.ch
	return 0, io.EOF
}

// TestManagerWritesSerialised proves same-session writes never overlap.
func TestManagerWritesSerialised(t *testing.T) {
	sink := &recordSink{}
	h := hosts.New(t.TempDir())
	hostID := addHost(t, h, "lab", "127.0.0.1:22", "user")
	conn := &trackingConn{done: make(chan struct{})}
	m := New(Deps{
		Hosts: h,
		Sink:  sink,
		Connector: ConnectorFunc(func(ctx context.Context, opts sshclient.Options) (Conn, error) {
			return conn, nil
		}),
	})
	res, err := m.Connect(context.Background(), hostID, ConnectOptions{Password: "x"})
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = m.Write(res.SessionID, "payload\n")
		}()
	}
	wg.Wait()
	conn.mu.Lock()
	max := conn.maxActive
	written := len(conn.writes)
	conn.mu.Unlock()
	if max != 1 {
		t.Fatalf("max concurrent writes = %d, want 1 (per-session writes must serialize)", max)
	}
	if written != 4 {
		t.Fatalf("writes delivered = %d, want 4", written)
	}
	// Release the pumps so the session can end and no goroutine leaks.
	close(conn.done)
	_ = m.Disconnect(res.SessionID)
}

// faultConn's stdout fails with a transport error so the pump must surface it
// as session:error followed by session:closed exactly once.
type faultConn struct{ done chan struct{} }

func (f *faultConn) Write([]byte) (int, error) { return 0, nil }
func (f *faultConn) Resize(int, int) error     { return nil }
func (f *faultConn) Wait() error               { <-f.done; return nil }
func (f *faultConn) Close() error              { return nil }
func (f *faultConn) Stdout() io.Reader         { return errReader{} }
func (f *faultConn) Stderr() io.Reader         { return &gateReader{ch: f.done} }
func (f *faultConn) Fingerprint() string       { return "" }

// errReader fails the first Read with a transport error.
type errReader struct{}

func (errReader) Read([]byte) (int, error) { return 0, errors.New("ssh: connection lost") }

// releaseReader delivers tail bytes then EOF once released; Read blocks until
// then, so a pump reading it cannot finish ahead of the release.
type releaseReader struct {
	once sync.Once
	wait chan struct{}
	mu   sync.Mutex
	tail []byte
}

func (r *releaseReader) Read(p []byte) (int, error) {
	<-r.wait
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.tail) > 0 {
		n := copy(p, r.tail)
		r.tail = r.tail[n:]
		return n, nil
	}
	return 0, io.EOF
}

func (r *releaseReader) release() {
	r.once.Do(func() { close(r.wait) })
}

// gateTailConn: Wait returns immediately (remote end) while both readers hold
// tail data that is only released later or by Close — proving that Wait never
// closes the batcher ahead of the output pumps.
type gateTailConn struct {
	stdout  *releaseReader
	stderr  *releaseReader
	waitErr error
	once    sync.Once
	release func()
}

func newGateTailConn(tail string) *gateTailConn {
	wait := make(chan struct{})
	c := &gateTailConn{
		stdout: &releaseReader{wait: wait, tail: []byte(tail)},
		stderr: &releaseReader{wait: wait},
	}
	c.release = func() {
		c.once.Do(func() { close(wait) })
	}
	return c
}

func (c *gateTailConn) Write([]byte) (int, error) { return 0, nil }
func (c *gateTailConn) Resize(int, int) error     { return nil }
func (c *gateTailConn) Wait() error               { return c.waitErr }
func (c *gateTailConn) Close() error              { c.release(); return nil }
func (c *gateTailConn) Stdout() io.Reader         { return c.stdout }
func (c *gateTailConn) Stderr() io.Reader         { return c.stderr }
func (c *gateTailConn) Fingerprint() string       { return "" }

// RED/GREEN: Wait returning (remote end) must not race ahead of the pumps and
// truncate the tail. The tail is released only AFTER Wait has returned; the
// full tail must arrive as data before session:closed.
func TestManagerTailDataBeforeClosed(t *testing.T) {
	const tail = "tail-output"
	sink := &recordSink{}
	h := hosts.New(t.TempDir())
	hostID := addHost(t, h, "lab", "127.0.0.1:22", "user")
	conn := newGateTailConn(tail)
	m := New(Deps{
		Hosts: h,
		Sink:  sink,
		Connector: ConnectorFunc(func(ctx context.Context, opts sshclient.Options) (Conn, error) {
			return conn, nil
		}),
	})
	if _, err := m.Connect(context.Background(), hostID, ConnectOptions{Password: "x"}); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	// Let Wait return and the end-path engage, then release the tail late.
	time.Sleep(100 * time.Millisecond)
	conn.release()
	waitFor(t, 3*time.Second, "closed after tail", func() bool { return sink.count(EventSessionClosed) == 1 })
	joined := sink.dataJoined()
	if joined != tail {
		t.Fatalf("tail data = %q, want the full %q (must not be truncated)", joined, tail)
	}
	closedIdx := sink.lastIndex(EventSessionClosed)
	if dataIdx := sink.lastIndex(EventSessionData); dataIdx == -1 || dataIdx > closedIdx {
		t.Fatalf("tail data event must precede session:closed (data idx %d, closed idx %d)", dataIdx, closedIdx)
	}
	if got := sink.count(EventSessionError); got != 0 {
		t.Fatalf("session:error emitted %d times on a clean remote end, want 0", got)
	}
}

// RED/GREEN: Disconnect must close the conn so the pumps exit; endOnce must
// not deadlock waiting for pumps that are blocked until the conn closes.
func TestManagerDisconnectReleasesPumpsNoDeadlock(t *testing.T) {
	sink := &recordSink{}
	h := hosts.New(t.TempDir())
	hostID := addHost(t, h, "lab", "127.0.0.1:22", "user")
	conn := newGateTailConn("")
	m := New(Deps{
		Hosts: h,
		Sink:  sink,
		Connector: ConnectorFunc(func(ctx context.Context, opts sshclient.Options) (Conn, error) {
			return conn, nil
		}),
	})
	res, err := m.Connect(context.Background(), hostID, ConnectOptions{Password: "x"})
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	done := make(chan struct{})
	go func() {
		_ = m.Disconnect(res.SessionID)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("Disconnect deadlocked: endOnce waited on pumps that only the conn close can release")
	}
	waitFor(t, 3*time.Second, "closed event", func() bool { return sink.count(EventSessionClosed) == 1 })
	if got := sink.count(EventSessionError); got != 0 {
		t.Fatalf("session:error emitted %d times on local disconnect, want 0", got)
	}
}

// waitFaultConn: Wait returns a genuine transport fault and releases both
// readers (the transport is dead, so the pumps see EOF); Close is the same
// release, idempotent.
type waitFaultConn struct {
	waitErr error
	done    chan struct{}
	once    sync.Once
}

func newWaitFaultConn(waitErr error) *waitFaultConn {
	return &waitFaultConn{waitErr: waitErr, done: make(chan struct{})}
}

func (f *waitFaultConn) Write([]byte) (int, error) { return 0, nil }
func (f *waitFaultConn) Resize(int, int) error     { return nil }
func (f *waitFaultConn) Wait() error {
	f.once.Do(func() { close(f.done) })
	return f.waitErr
}
func (f *waitFaultConn) Close() error {
	f.once.Do(func() { close(f.done) })
	return nil
}
func (f *waitFaultConn) Stdout() io.Reader   { return &gateReader{ch: f.done} }
func (f *waitFaultConn) Stderr() io.Reader   { return &gateReader{ch: f.done} }
func (f *waitFaultConn) Fingerprint() string { return "" }

// testWaitFault drives one waitFaultConn through the manager and asserts the
// fault surfaces as exactly one session:error followed by session:closed.
func testWaitFault(t *testing.T, waitErr error, label string) {
	t.Helper()
	sink := &recordSink{}
	h := hosts.New(t.TempDir())
	hostID := addHost(t, h, "lab", "127.0.0.1:22", "user")
	conn := newWaitFaultConn(waitErr)
	m := New(Deps{
		Hosts: h,
		Sink:  sink,
		Connector: ConnectorFunc(func(ctx context.Context, opts sshclient.Options) (Conn, error) {
			return conn, nil
		}),
	})
	_, err := m.Connect(context.Background(), hostID, ConnectOptions{Password: "x"})
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	waitFor(t, 3*time.Second, "error event for "+label, func() bool { return sink.count(EventSessionError) >= 1 })
	waitFor(t, 3*time.Second, "closed event for "+label, func() bool { return sink.count(EventSessionClosed) == 1 })
	if got := sink.count(EventSessionError); got != 1 {
		t.Fatalf("session:error emitted %d times on %s, want exactly 1", got, label)
	}
	if got := sink.count(EventSessionClosed); got != 1 {
		t.Fatalf("session:closed emitted %d times on %s, want exactly 1", got, label)
	}
	sink.mu.Lock()
	names := make([]string, 0, len(sink.events))
	for _, e := range sink.events {
		names = append(names, e.name)
	}
	sink.mu.Unlock()
	if len(names) != 2 || names[0] != EventSessionError || names[1] != EventSessionClosed {
		t.Fatalf("event order on %s = %v, want [%s %s]", label, names, EventSessionError, EventSessionClosed)
	}
}

// RED/GREEN: a remote TCP reset (ECONNRESET wrapped in net.OpError) is a
// genuine transport fault — it must surface as session:error followed by
// session:closed, never be silenced by string-matching "connection reset".
func TestManagerRemoteResetEmitsErrorThenClosed(t *testing.T) {
	opErr := &net.OpError{Op: "read", Net: "tcp", Addr: &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 22}, Err: syscall.ECONNRESET}
	testWaitFault(t, opErr, "remote reset")
}

// RED/GREEN: a broken pipe (write to a connection the remote just closed) must
// also surface as session:error — it is not a local teardown side effect.
func TestManagerBrokenPipeEmitsErrorThenClosed(t *testing.T) {
	opErr := &net.OpError{Op: "write", Net: "tcp", Addr: &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 22}, Err: syscall.EPIPE}
	testWaitFault(t, opErr, "broken pipe")
}

// TestManagerTransportFaultEmitsErrorThenClosedOnce: a genuine transport fault
// surfaces as session:error (UNKNOWN) followed by exactly one session:closed.
func TestManagerTransportFaultEmitsErrorThenClosedOnce(t *testing.T) {
	sink := &recordSink{}
	h := hosts.New(t.TempDir())
	hostID := addHost(t, h, "lab", "127.0.0.1:22", "user")
	conn := &faultConn{done: make(chan struct{})}
	m := New(Deps{
		Hosts: h,
		Sink:  sink,
		Connector: ConnectorFunc(func(ctx context.Context, opts sshclient.Options) (Conn, error) {
			return conn, nil
		}),
	})
	res, err := m.Connect(context.Background(), hostID, ConnectOptions{Password: "x"})
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	waitFor(t, 3*time.Second, "error event", func() bool { return sink.count(EventSessionError) >= 1 })
	waitFor(t, 3*time.Second, "closed event", func() bool { return sink.count(EventSessionClosed) == 1 })

	if got := sink.count(EventSessionError); got != 1 {
		t.Fatalf("session:error emitted %d times, want exactly 1", got)
	}
	if got := sink.count(EventSessionClosed); got != 1 {
		t.Fatalf("session:closed emitted %d times, want exactly 1", got)
	}
	sink.mu.Lock()
	names := make([]string, 0, len(sink.events))
	for _, e := range sink.events {
		names = append(names, e.name)
	}
	sink.mu.Unlock()
	if len(names) != 2 || names[0] != EventSessionError || names[1] != EventSessionClosed {
		t.Fatalf("event order = %v, want [%s %s]", names, EventSessionError, EventSessionClosed)
	}
	close(conn.done)
	_ = m.Disconnect(res.SessionID)
}

// RED/GREEN: the endOnce race must never swallow a real fault. A fault that a
// pump has already recorded (its end(err) started, err != nil) must still
// surface as session:error even when a concurrent Disconnect's end(nil) wins
// endOnce first. recordFault and finish are separate methods so this ordering
// is testable deterministically.
func TestManagerFaultRecordedBeforeNilEndStillEmitsError(t *testing.T) {
	sink := &recordSink{}
	m := New(Deps{Hosts: hosts.New(t.TempDir()), Sink: sink})
	conn := &fpConn{closed: make(chan struct{})}
	sess := &Session{
		manager: m,
		ID:      "s1",
		conn:    conn,
		batcher: newOutputBatcher(time.Hour, 1024, func([]byte) {}),
	}
	m.mu.Lock()
	m.sessions[sess.ID] = sess
	m.mu.Unlock()

	// The pump's fault call has begun (fault recorded) when Disconnect's
	// clean end(nil) grabs the once.
	sess.recordFault(&Error{Code: apperror.Timeout, Message: "Connection timed out"})
	sess.end(nil)

	if got := sink.count(EventSessionClosed); got != 1 {
		t.Fatalf("session:closed emitted %d times, want exactly 1", got)
	}
	if got := sink.count(EventSessionError); got != 1 {
		t.Fatalf("session:error emitted %d times, want exactly 1 (recorded fault must survive the nil end)", got)
	}
	sink.mu.Lock()
	names := make([]string, 0, len(sink.events))
	var code string
	for _, e := range sink.events {
		names = append(names, e.name)
		if e.name == EventSessionError {
			if ev, ok := e.payload.(ErrorEvent); ok {
				code = ev.Error.Code
			}
		}
	}
	sink.mu.Unlock()
	if len(names) != 2 || names[0] != EventSessionError || names[1] != EventSessionClosed {
		t.Fatalf("event order = %v, want [%s %s]", names, EventSessionError, EventSessionClosed)
	}
	if code != apperror.Timeout {
		t.Fatalf("session:error code = %q, want %q", code, apperror.Timeout)
	}
}

// TestConnectDuringDisposeAllWorkerExits verifies that when a connect race finishes
// after DisposeAll sets closing=true, the session batcher is discarded and its worker terminates.
func TestConnectDuringDisposeAllWorkerExits(t *testing.T) {
	sink := &recordSink{}
	h := hosts.New(t.TempDir())
	hostID := addHost(t, h, "srv", "127.0.0.1:22", "user")
	conn := &fpConn{closed: make(chan struct{})}

	m := New(Deps{
		Hosts: h,
		Sink:  sink,
		Connector: ConnectorFunc(func(ctx context.Context, opts sshclient.Options) (Conn, error) {
			return conn, nil
		}),
	})

	// Set manager to closing state (simulate DisposeAll)
	m.DisposeAll()

	// Attempt connect: should be cancelled and batcher worker must exit cleanly
	_, err := m.Connect(context.Background(), hostID, ConnectOptions{Password: "pw"})
	assertErrorCode(t, err, apperror.Cancelled)
}

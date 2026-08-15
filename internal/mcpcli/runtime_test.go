package mcpcli

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"nodeshell/internal/apperror"
	"nodeshell/internal/hosts"
	"nodeshell/internal/sessions"
	"nodeshell/internal/sftpservice"
)

// TestConnectHostSuccessAndTitle: a successful connect returns the session id
// and a username@host title, and the metadata is listed.
func TestConnectHostSuccessAndTitle(t *testing.T) {
	m := newFakeManager()
	rt := newTestRuntime(2, time.Minute, m, &fakeSFTP{}, nil)

	res, err := rt.ConnectHost(context.Background(), "h1", sessions.ConnectOptions{})
	if err != nil {
		t.Fatalf("ConnectHost: %v", err)
	}
	if res.Title != "user@192.0.2.10" {
		t.Fatalf("title = %q, want user@192.0.2.10", res.Title)
	}
	sessionsList := rt.ListSessions()
	if len(sessionsList) != 1 || sessionsList[0].SessionID != res.SessionID {
		t.Fatalf("ListSessions = %+v, want the connected session", sessionsList)
	}
	if sessionsList[0].HostID != "h1" || sessionsList[0].Title != "user@192.0.2.10" {
		t.Fatalf("session metadata = %+v", sessionsList[0])
	}
}

// TestConnectHostPassesCredentialsThrough: the password and acceptHostKey
// options reach the manager untouched and never enter the metadata.
func TestConnectHostPassesCredentialsThrough(t *testing.T) {
	m := newFakeManager()
	rt := newTestRuntime(2, time.Minute, m, &fakeSFTP{}, nil)

	if _, err := rt.ConnectHost(context.Background(), "h1", sessions.ConnectOptions{Password: "sekret", AcceptHostKey: true}); err != nil {
		t.Fatalf("ConnectHost: %v", err)
	}
	_, opts, _ := m.snapshot()
	if opts.Password != "sekret" || !opts.AcceptHostKey {
		t.Fatalf("manager options = %+v, want password+acceptHostKey passed through", opts)
	}
	for _, s := range rt.ListSessions() {
		if s.Title == "" || s.Title != "user@192.0.2.10" {
			t.Fatalf("title metadata must never hold a password, got %+v", s)
		}
	}
}

// TestConnectHostUnknownHost: an unknown host id is a coded HOST_NOT_FOUND.
func TestConnectHostUnknownHost(t *testing.T) {
	m := newFakeManager()
	rt := newTestRuntime(2, time.Minute, m, &fakeSFTP{}, nil)
	_, err := rt.ConnectHost(context.Background(), "nope", sessions.ConnectOptions{})
	assertErrorCode(t, err, apperror.HostNotFound)
}

// TestConnectHostLimitEstablished: the max-sessions policy is enforced on
// established sessions.
func TestConnectHostLimitEstablished(t *testing.T) {
	m := newFakeManager()
	rt := newTestRuntime(2, time.Minute, m, &fakeSFTP{}, nil)
	connectOK(t, rt, "h1")
	connectOK(t, rt, "h1")
	_, err := rt.ConnectHost(context.Background(), "h1", sessions.ConnectOptions{})
	assertErrorCode(t, err, apperror.McpSessionLimit)
}

// TestConnectHostConcurrentReserve: with maxSessions in-flight connects, the
// next concurrent connect is refused with MCP_SESSION_LIMIT before any
// manager call; the in-flight ones complete and the limit then applies to
// established sessions too.
func TestConnectHostConcurrentReserve(t *testing.T) {
	const max = 8
	m := newFakeManager()
	m.connectStart = make(chan struct{}, max)
	m.connectBlock = make(chan struct{})
	rt := newTestRuntime(max, time.Minute, m, &fakeSFTP{}, nil)

	var wg sync.WaitGroup
	for i := 0; i < max; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := rt.ConnectHost(context.Background(), "h1", sessions.ConnectOptions{}); err != nil {
				t.Errorf("ConnectHost within limit: %v", err)
			}
		}()
	}
	// Wait until all max connects are in-flight (reserved) in the manager.
	for i := 0; i < max; i++ {
		waitChan(t, m.connectStart, "connect start")
	}
	// A 9th concurrent connect must fail on the reserve, before any manager call.
	if _, err := rt.ConnectHost(context.Background(), "h1", sessions.ConnectOptions{}); err == nil {
		t.Fatal("9th concurrent connect must be refused")
	} else {
		assertErrorCode(t, err, apperror.McpSessionLimit)
	}
	close(m.connectBlock)
	wg.Wait()

	if got := len(rt.ListSessions()); got != max {
		t.Fatalf("established sessions = %d, want %d", got, max)
	}
	// After establishment the limit applies to the map as well.
	_, err := rt.ConnectHost(context.Background(), "h1", sessions.ConnectOptions{})
	assertErrorCode(t, err, apperror.McpSessionLimit)
}

// TestConnectHostFailureReleasesReserve: a failed connect frees its pending
// slot so later connects can proceed.
func TestConnectHostFailureReleasesReserve(t *testing.T) {
	m := newFakeManager()
	rt := newTestRuntime(2, time.Minute, m, &fakeSFTP{}, nil)

	connectOK(t, rt, "h1")
	connectOK(t, rt, "h1") // max=2, now full
	_, err := rt.ConnectHost(context.Background(), "h1", sessions.ConnectOptions{})
	assertErrorCode(t, err, apperror.McpSessionLimit)

	// Disconnect frees the slot.
	rt.DisconnectSession(rt.ListSessions()[0].SessionID)
	connectOK(t, rt, "h1")

	// A connect whose manager fails must also release the reserve: free a
	// slot first, then a manager failure leaves no pending reservation behind.
	rt.DisconnectSession(rt.ListSessions()[0].SessionID)
	m.mu.Lock()
	m.connectErr = &sessions.Error{Code: apperror.AuthFailed, Message: "auth"}
	m.mu.Unlock()
	if _, err := rt.ConnectHost(context.Background(), "h1", sessions.ConnectOptions{}); err == nil {
		t.Fatal("connect with manager failure must error")
	}
	m.mu.Lock()
	m.connectErr = nil
	m.mu.Unlock()
	// The slot was released: another connect succeeds.
	connectOK(t, rt, "h1")
}

// TestListSessionsInsertionOrder: the session array follows insertion order
// (TS Map semantics) and stays deterministic after removals.
func TestListSessionsInsertionOrder(t *testing.T) {
	m := newFakeManager()
	rt := newTestRuntime(8, time.Minute, m, &fakeSFTP{}, nil)
	a := connectOK(t, rt, "h1")
	b := connectOK(t, rt, "h1")
	c := connectOK(t, rt, "h1")

	got := rt.ListSessions()
	if len(got) != 3 || got[0].SessionID != a || got[1].SessionID != b || got[2].SessionID != c {
		t.Fatalf("ListSessions order = %+v, want [%s %s %s]", got, a, b, c)
	}
	rt.DisconnectSession(b)
	got = rt.ListSessions()
	if len(got) != 2 || got[0].SessionID != a || got[1].SessionID != c {
		t.Fatalf("ListSessions after removal = %+v, want [%s %s]", got, a, c)
	}
}

// TestDisconnectUnknownNoOp: disconnecting an unknown id is a silent no-op.
func TestDisconnectUnknownNoOp(t *testing.T) {
	m := newFakeManager()
	rt := newTestRuntime(2, time.Minute, m, &fakeSFTP{}, nil)
	rt.DisconnectSession("nope")
	if len(rt.ListSessions()) != 0 {
		t.Fatal("disconnect of unknown id must not create sessions")
	}
}

// TestRunCommandClampsTimeout: timeoutMs defaults to 60000 and clamps into
// [1, 300000]; the output passes through.
func TestRunCommandClampsTimeout(t *testing.T) {
	m := newFakeManager()
	m.execOut = "out"
	rt := newTestRuntime(2, time.Minute, m, &fakeSFTP{}, nil)
	sid := connectOK(t, rt, "h1")

	cases := []struct {
		ms   int64
		want time.Duration
	}{
		{0, 60000 * time.Millisecond},
		{-50, 60000 * time.Millisecond},
		{150, 150 * time.Millisecond},
		{300000, 300000 * time.Millisecond},
		{500000, 300000 * time.Millisecond},
	}
	for _, tc := range cases {
		out, err := rt.RunCommand(context.Background(), sid, "ls", tc.ms)
		if err != nil {
			t.Fatalf("RunCommand(timeoutMs=%d): %v", tc.ms, err)
		}
		if out != "out" {
			t.Fatalf("RunCommand output = %q, want %q", out, m.execOut)
		}
	}
	_, _, timeouts := m.snapshot()
	for i, tc := range cases {
		if timeouts[i] != tc.want {
			t.Fatalf("RunCommand %d timeout = %v, want %v", i, timeouts[i], tc.want)
		}
	}
}

// TestRunCommandUnknownSession: running a command on an unknown session is a
// coded SESSION_NOT_FOUND.
func TestRunCommandUnknownSession(t *testing.T) {
	m := newFakeManager()
	rt := newTestRuntime(2, time.Minute, m, &fakeSFTP{}, nil)
	_, err := rt.RunCommand(context.Background(), "ghost", "ls", 0)
	assertErrorCode(t, err, apperror.SessionNotFound)
}

// TestRunCommandCodedManagerError: a coded error from the manager (e.g.
// TIMEOUT) is preserved.
func TestRunCommandCodedManagerError(t *testing.T) {
	m := newFakeManager()
	m.execErr = &sessions.Error{Code: apperror.Timeout, Message: "Command timed out"}
	rt := newTestRuntime(2, time.Minute, m, &fakeSFTP{}, nil)
	sid := connectOK(t, rt, "h1")
	_, err := rt.RunCommand(context.Background(), sid, "sleep 1", 100)
	assertErrorCode(t, err, apperror.Timeout)
}

// TestReapIdleOnly: Reap closes sessions idle >= timeout with busyCount == 0
// and never a busy session; lastUsed is refreshed by operations.
func TestReapIdleOnly(t *testing.T) {
	clk := newSettableClock()
	m := newFakeManager()
	m.execStart = make(chan struct{}, 1)
	m.execBlock = make(chan struct{})
	rt := newTestRuntime(4, time.Minute, m, &fakeSFTP{}, clk.now)
	sidBusy := connectOK(t, rt, "h1")
	sidIdle := connectOK(t, rt, "h1")

	// Hold the busy session mid-command (operation bumps busy + lastUsed).
	execDone := make(chan struct{})
	go func() {
		defer close(execDone)
		if _, err := rt.RunCommand(context.Background(), sidBusy, "sleep", 0); err != nil {
			t.Errorf("RunCommand: %v", err)
		}
	}()
	waitChan(t, m.execStart, "exec start")

	clk.advance(time.Minute) // exactly the idle timeout

	closed := rt.Reap(clk.now())
	if len(closed) != 1 || closed[0] != sidIdle {
		t.Fatalf("Reap = %v, want only the idle session %s", closed, sidIdle)
	}
	if got := rt.ListSessions(); len(got) != 1 || got[0].SessionID != sidBusy {
		t.Fatalf("ListSessions after reap = %+v, want the busy session kept", got)
	}

	// Reap again: the busy session is still not reaped while busy.
	if closed := rt.Reap(clk.now()); len(closed) != 0 {
		t.Fatalf("busy session must not be reaped, got %v", closed)
	}
	close(m.execBlock)
	waitChan(t, execDone, "exec done")

	// After the command finishes the session becomes idle; lastUsed was
	// refreshed at operation end, so one more minute is needed.
	if closed := rt.Reap(clk.now()); len(closed) != 0 {
		t.Fatalf("session reaped before the post-command lastUsed elapsed: %v", closed)
	}
	clk.advance(time.Minute)
	closed = rt.Reap(clk.now())
	if len(closed) != 1 || closed[0] != sidBusy {
		t.Fatalf("Reap after op end = %v, want %s", closed, sidBusy)
	}
}

// TestReapBoundary: idle exactly at the timeout is reaped; just below is kept.
func TestReapBoundary(t *testing.T) {
	clk := newSettableClock()
	m := newFakeManager()
	rt := newTestRuntime(4, 2*time.Minute, m, &fakeSFTP{}, clk.now)
	sid := connectOK(t, rt, "h1")

	clk.advance(2*time.Minute - time.Second)
	if closed := rt.Reap(clk.now()); len(closed) != 0 {
		t.Fatalf("session reaped below the idle timeout: %v", closed)
	}
	clk.advance(time.Second)
	closed := rt.Reap(clk.now())
	if len(closed) != 1 || closed[0] != sid {
		t.Fatalf("Reap at the boundary = %v, want %s", closed, sid)
	}
}

// TestReapReturnsClosedIds: Reap returns the ids it closed, in order.
func TestReapReturnsClosedIds(t *testing.T) {
	clk := newSettableClock()
	m := newFakeManager()
	rt := newTestRuntime(4, time.Minute, m, &fakeSFTP{}, clk.now)
	a := connectOK(t, rt, "h1")
	b := connectOK(t, rt, "h1")
	clk.advance(time.Minute)
	closed := rt.Reap(clk.now())
	if len(closed) != 2 || closed[0] != a || closed[1] != b {
		t.Fatalf("Reap = %v, want [%s %s]", closed, a, b)
	}
	if len(rt.ListSessions()) != 0 {
		t.Fatalf("ListSessions after reap = %+v, want empty", rt.ListSessions())
	}
}

// TestRemoteCloseRemovesMetadata: a session:closed event (remote fault) drops
// the metadata and disposes the SFTP handle; list_sessions updates.
func TestRemoteCloseRemovesMetadata(t *testing.T) {
	m := newFakeManager()
	sf := &fakeSFTP{}
	rt := newTestRuntime(2, time.Minute, m, sf, nil)
	sid := connectOK(t, rt, "h1")

	// A real sessions.Manager would emit this from its Wait pump; here the
	// runtime sink is driven directly with the same payload type.
	rt.Sink().Emit(sessions.EventSessionClosed, sessions.ClosedEvent{SessionID: sid})

	if got := rt.ListSessions(); len(got) != 0 {
		t.Fatalf("ListSessions after remote close = %+v, want empty", got)
	}
	_, _, _, _, disposed := sf.snapshot()
	if len(disposed) != 1 || disposed[0] != sid {
		t.Fatalf("sftp disposed = %v, want [%s]", disposed, sid)
	}
	if _, err := rt.RunCommand(context.Background(), sid, "ls", 0); err == nil {
		t.Fatal("RunCommand on a remote-closed session must fail")
	} else {
		assertErrorCode(t, err, apperror.SessionNotFound)
	}
}

// TestSftpOpsBumpBusy: an in-flight sftp operation keeps the session from
// being reaped (close-under-op protection for every tool operation).
func TestSftpOpsBumpBusy(t *testing.T) {
	clk := newSettableClock()
	m := newFakeManager()
	sf := &fakeSFTP{listErr: &Error{Code: apperror.Unknown, Message: "boom"}}
	// Make List block so the operation is in-flight while we reap.
	blocked := make(chan struct{})
	release := make(chan struct{})
	sf.listErr = nil
	var blockedSFTP SFTP = &blockingSFTP{inner: sf, started: blocked, release: release}

	rt := newTestRuntime(4, time.Minute, m, blockedSFTP, clk.now)
	sid := connectOK(t, rt, "h1")

	done := make(chan struct{})
	go func() {
		defer close(done)
		if _, err := rt.SftpList(context.Background(), sid, ""); err != nil {
			t.Errorf("SftpList: %v", err)
		}
	}()
	waitChan(t, blocked, "sftp list start")

	clk.advance(time.Minute)
	if closed := rt.Reap(clk.now()); len(closed) != 0 {
		t.Fatalf("session must not be reaped mid-sftp-operation, got %v", closed)
	}
	close(release)
	waitChan(t, done, "sftp list done")
}

// blockingSFTP wraps a fakeSFTP and blocks List until release is closed.
type blockingSFTP struct {
	inner   SFTP
	started chan struct{}
	release chan struct{}
}

func (b *blockingSFTP) Chdir(sessionID, remotePath string) (string, error) {
	return b.inner.Chdir(sessionID, remotePath)
}
func (b *blockingSFTP) Cwd(sessionID string) (string, error) { return b.inner.Cwd(sessionID) }
func (b *blockingSFTP) List(sessionID, remotePath string) ([]sftpservice.Entry, error) {
	select {
	case b.started <- struct{}{}:
	default:
	}
	<-b.release
	return b.inner.List(sessionID, remotePath)
}
func (b *blockingSFTP) ReadText(sessionID, remotePath string, maxBytes int64) (string, string, error) {
	return b.inner.ReadText(sessionID, remotePath, maxBytes)
}
func (b *blockingSFTP) WriteText(sessionID, remotePath, content string, maxBytes int64) (string, error) {
	return b.inner.WriteText(sessionID, remotePath, content, maxBytes)
}
func (b *blockingSFTP) UploadAs(sessionID, localPath, remoteName string) error {
	return b.inner.UploadAs(sessionID, localPath, remoteName)
}
func (b *blockingSFTP) Download(sessionID, remotePath, localPath string) error {
	return b.inner.Download(sessionID, remotePath, localPath)
}
func (b *blockingSFTP) Dispose(sessionID string) { b.inner.Dispose(sessionID) }
func (b *blockingSFTP) Interrupt(sessionID string) {
	b.inner.Interrupt(sessionID)
}

// TestDisposeAll: DisposeAll stops the reaper, disconnects every session and
// disposes every SFTP handle; a subsequent operation is SESSION_NOT_FOUND.
func TestDisposeAll(t *testing.T) {
	clk := newSettableClock()
	m := newFakeManager()
	sf := &fakeSFTP{}
	rt := newTestRuntime(4, time.Minute, m, sf, clk.now)
	a := connectOK(t, rt, "h1")
	b := connectOK(t, rt, "h1")

	rt.StartReaper(time.Minute)
	rt.DisposeAll()

	if got := rt.ListSessions(); len(got) != 0 {
		t.Fatalf("ListSessions after DisposeAll = %+v, want empty", got)
	}
	closed, _, _ := m.snapshot()
	if len(closed) != 2 {
		t.Fatalf("manager disconnects = %v, want both sessions", closed)
	}
	_, _, _, _, disposed := sf.snapshot()
	if len(disposed) != 2 {
		t.Fatalf("sftp disposed = %v, want both sessions", disposed)
	}
	// DisposeAll is idempotent and the reaper is already joined.
	rt.DisposeAll()
	if _, err := rt.RunCommand(context.Background(), a, "ls", 0); err == nil {
		t.Fatal("RunCommand after DisposeAll must fail")
	} else {
		assertErrorCode(t, err, apperror.SessionNotFound)
	}
	_ = b
}

// TestReaperStartStopJoin: StartReaper/StopReaper can be called multiple
// times and StopReaper joins the loop.
func TestReaperStartStopJoin(t *testing.T) {
	m := newFakeManager()
	rt := newTestRuntime(4, time.Minute, m, &fakeSFTP{}, nil)
	rt.StartReaper(time.Hour)
	rt.StopReaper()
	rt.StopReaper() // idempotent
	rt.StartReaper(time.Hour)
	rt.StopReaper()
}

// TestPolicyClamps: zero policy values fall back to the product defaults
// (max 8 sessions, 10 minute idle timeout).
func TestPolicyClamps(t *testing.T) {
	clk := newSettableClock()
	m := newFakeManager()
	rt := New(Deps{Hosts: newFakeHostStore(testHost("h1", "lab")), Manager: m, SFTP: &fakeSFTP{}, Clock: clk.now})

	// maxSessions defaulted to 8: 8 connects succeed, the 9th is refused.
	for i := 0; i < 8; i++ {
		connectOK(t, rt, "h1")
	}
	if _, err := rt.ConnectHost(context.Background(), "h1", sessions.ConnectOptions{}); err == nil {
		t.Fatal("9th session must be refused with the default max")
	} else {
		assertErrorCode(t, err, apperror.McpSessionLimit)
	}

	// idleTimeout defaulted to 10 minutes: 9 minutes is not idle, 10 is.
	// (DisposeAll permanently closes the runtime, so free the slots by
	// disconnecting each session instead.)
	for _, s := range rt.ListSessions() {
		rt.DisconnectSession(s.SessionID)
	}
	clk.advance(time.Minute) // ensure the clock has moved past the connects above
	sid := connectOK(t, rt, "h1")
	clk.advance(9 * time.Minute)
	if closed := rt.Reap(clk.now()); len(closed) != 0 {
		t.Fatalf("session reaped below the default 10m timeout: %v", closed)
	}
	clk.advance(time.Minute)
	if closed := rt.Reap(clk.now()); len(closed) != 1 || closed[0] != sid {
		t.Fatalf("Reap at 10m = %v, want %s", closed, sid)
	}
}

// TestListHostsDTO: list_hosts returns exactly the seven DTO fields, never a
// secret (private key path or credentials).
func TestListHostsDTO(t *testing.T) {
	store := newFakeHostStore(hosts.HostConfig{
		Id: "h1", Name: "lab", Host: "192.0.2.10", Port: 2222,
		Username: "user", AuthMethod: "privateKey",
		PrivateKeyPath: "C:\\secrets\\id_rsa", CredentialsSaved: true,
	})
	m := newFakeManager()
	rt := New(Deps{Hosts: store, Manager: m, SFTP: &fakeSFTP{}, MaxSessions: 2, IdleTimeout: time.Minute})

	got, err := rt.ListHosts()
	if err != nil {
		t.Fatalf("ListHosts: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("ListHosts = %+v", got)
	}
	want := HostDTO{ID: "h1", Name: "lab", Host: "192.0.2.10", Port: 2222, Username: "user", AuthMethod: "privateKey", CredentialsSaved: true}
	if got[0] != want {
		t.Fatalf("ListHosts[0] = %+v, want %+v (no secrets)", got[0], want)
	}
}

// TestStopReaperConcurrentDoubleClose (F1): concurrent StopReaper calls must
// never double-close the reap stop channel. The reaper generation's WaitGroup
// is pinned so the first caller parks inside Wait with the stop channel still
// detached-owned; the second caller must see nothing to stop and return
// without closing anything. The old implementation panics "close of closed
// channel".
func TestStopReaperConcurrentDoubleClose(t *testing.T) {
	rt := newTestRuntime(4, time.Minute, newFakeManager(), &fakeSFTP{}, nil)
	rt.StartReaper(time.Hour)
	gen := rt.reap
	gen.wg.Add(1) // pin: the first StopReaper parks in Wait below

	// Caller 1: detaches the generation and parks inside Wait.
	firstDone := make(chan struct{})
	go func() {
		defer close(firstDone)
		rt.StopReaper()
	}()

	// Deterministically wait until caller 1 has detached the generation
	// (reap is nil again under reapMu) and is parked in Wait.
	deadline := time.After(5 * time.Second)
	for {
		rt.reapMu.Lock()
		detached := rt.reap == nil
		rt.reapMu.Unlock()
		if detached {
			break
		}
		select {
		case <-deadline:
			t.Fatal("first StopReaper did not detach the generation")
		case <-time.After(time.Millisecond):
		}
	}

	// Caller 2: sees nothing to stop and must return without closing the
	// stop channel (the double-close panic point of the old code).
	rt.StopReaper()

	// Unpin so caller 1 can finish its join.
	gen.wg.Done()
	select {
	case <-firstDone:
	case <-time.After(5 * time.Second):
		t.Fatal("first StopReaper did not return after the pin was released")
	}
	// Fully joined and restartable.
	rt.StartReaper(time.Hour)
	rt.StopReaper()
}

// TestDisposeAllConcurrent: concurrent DisposeAll calls (each of which stops
// the reaper) are safe and idempotent.
func TestDisposeAllConcurrent(t *testing.T) {
	clk := newSettableClock()
	m := newFakeManager()
	rt := newTestRuntime(4, time.Minute, m, &fakeSFTP{}, clk.now)
	connectOK(t, rt, "h1")
	rt.StartReaper(time.Minute)

	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			rt.DisposeAll()
		}()
	}
	wg.Wait()
	if got := rt.ListSessions(); len(got) != 0 {
		t.Fatalf("ListSessions after concurrent DisposeAll = %+v, want empty", got)
	}
	if _, err := rt.ConnectHost(context.Background(), "h1", sessions.ConnectOptions{}); err == nil {
		t.Fatal("connect after DisposeAll must be refused")
	}
}

// TestStartReaperDisposeAllRaceNoResurrect (T1.7.3 RED): StartReaper reads
// closed under mu and only later publishes the reap generation under reapMu.
// A DisposeAll landing between the two sets closed, stops nothing (reap is
// still nil) and returns; the late publish then resurrects a reap loop on a
// closed runtime. The beforeReaperPublish seam pins that window: with
// StartReaper parked there, DisposeAll must not return early, and once
// StartReaper is released the closed runtime must hold no reap generation.
func TestStartReaperDisposeAllRaceNoResurrect(t *testing.T) {
	rt := newTestRuntime(4, time.Minute, newFakeManager(), &fakeSFTP{}, nil)

	entered := make(chan struct{})
	release := make(chan struct{})
	rt.beforeReaperPublish = func() {
		close(entered)
		<-release
	}

	startDone := make(chan struct{})
	go func() {
		defer close(startDone)
		rt.StartReaper(time.Hour)
	}()
	waitChan(t, entered, "reaper publish hook")

	disposeDone := make(chan struct{})
	go func() {
		defer close(disposeDone)
		rt.DisposeAll()
	}()

	// With the publish serialised behind reapMu, DisposeAll must wait for the
	// in-flight StartReaper instead of returning while a publish is pending.
	select {
	case <-disposeDone:
		t.Logf("DisposeAll returned while StartReaper was parked at the publish hook (old-code window)")
	case <-time.After(200 * time.Millisecond):
		t.Logf("DisposeAll correctly blocked until the reaper published")
	}

	close(release) // let StartReaper publish
	waitChan(t, startDone, "StartReaper return")
	select {
	case <-disposeDone:
	case <-time.After(5 * time.Second):
		t.Fatal("DisposeAll did not return after StartReaper published")
	}

	// A closed runtime must have no reap generation: DisposeAll must have
	// detached and joined any loop that was published before it.
	defer rt.StopReaper() // join a leaked loop on the old implementation; no-op on the fix
	rt.reapMu.Lock()
	gen := rt.reap
	rt.reapMu.Unlock()
	if gen != nil {
		t.Fatalf("reaper generation %p survived DisposeAll on a closed runtime", gen)
	}
}

// TestReapBeginTOCTOU (F2 RED): once the reaper has selected idle sessions,
// an operation on one of them must fail before reaching the manager — a
// session whose teardown started can never accept a new op. The reaper is
// frozen in the fake manager's Disconnect of the first session while an op
// tries to begin on the second (still registered) session.
func TestReapBeginTOCTOU(t *testing.T) {
	clk := newSettableClock()
	m := newFakeManager()
	m.execStart = make(chan struct{}, 1)
	m.disconnectStart = make(chan struct{}, 1)
	m.disconnectBlock = make(chan struct{})
	rt := newTestRuntime(4, time.Minute, m, &fakeSFTP{}, clk.now)
	a := connectOK(t, rt, "h1")
	b := connectOK(t, rt, "h1")
	clk.advance(time.Minute) // both idle at the timeout

	reapDone := make(chan struct{})
	go func() {
		defer close(reapDone)
		rt.Reap(clk.now())
	}()
	// The reaper has selected a and b and is blocked tearing down a (first
	// in insertion order). b's metadata is still registered.
	waitChan(t, m.disconnectStart, "reaper disconnect start")

	opDone := make(chan struct{})
	var opErr error
	go func() {
		defer close(opDone)
		_, opErr = rt.RunCommand(context.Background(), b, "ls", 0)
	}()

	select {
	case <-m.execStart:
		t.Fatalf("op on %s reached the manager while the reaper had selected it", b)
	case <-opDone:
		// op rejected before the manager — the correct linearisation
	case <-time.After(5 * time.Second):
		t.Fatal("RunCommand neither reached the manager nor returned")
	}
	close(m.disconnectBlock)
	waitChan(t, reapDone, "reap done")
	waitChan(t, opDone, "op done")
	assertErrorCode(t, opErr, apperror.SessionNotFound)
	_ = a
}

// TestDisposeAllDuringConnectNoResurrect (F3 RED): a connect in flight when
// DisposeAll runs must not resurrect a session when it completes.
func TestDisposeAllDuringConnectNoResurrect(t *testing.T) {
	m := newFakeManager()
	m.connectStart = make(chan struct{}, 1)
	m.connectBlock = make(chan struct{})
	rt := newTestRuntime(4, time.Minute, m, &fakeSFTP{}, nil)

	connectDone := make(chan struct{})
	var connectErr error
	go func() {
		defer close(connectDone)
		_, connectErr = rt.ConnectHost(context.Background(), "h1", sessions.ConnectOptions{})
	}()
	waitChan(t, m.connectStart, "connect start")

	rt.DisposeAll()
	close(m.connectBlock) // the in-flight connect now completes
	waitChan(t, connectDone, "connect done")

	if connectErr == nil {
		t.Fatalf("connect finishing after DisposeAll must fail")
	}
	if got := rt.ListSessions(); len(got) != 0 {
		t.Fatalf("session resurrected after DisposeAll: %+v", got)
	}
	closed, _, _ := m.snapshot()
	if len(closed) != 1 {
		t.Fatalf("manager disconnects = %v, want the late session disconnected", closed)
	}
}

// TestConnectAfterDisposeAllRejected (F3 RED): after DisposeAll the runtime
// is closed; a new connect is refused before any manager call.
func TestConnectAfterDisposeAllRejected(t *testing.T) {
	m := newFakeManager()
	rt := newTestRuntime(4, time.Minute, m, &fakeSFTP{}, nil)
	rt.DisposeAll()
	_, err := rt.ConnectHost(context.Background(), "h1", sessions.ConnectOptions{})
	assertErrorCode(t, err, apperror.Cancelled)
}

// TestConnectClosedBeforeInsert (tombstone RED): when the manager emits
// session:closed for a brand-new session before ConnectHost inserts its
// metadata, the connect must fail and no phantom session may be listed.
func TestConnectClosedBeforeInsert(t *testing.T) {
	m := &connectEmittingManager{fakeManager: newFakeManager()}
	rt := newTestRuntime(2, time.Minute, m, &fakeSFTP{}, nil)
	m.sink = rt.Sink()

	_, err := rt.ConnectHost(context.Background(), "h1", sessions.ConnectOptions{})
	if err == nil {
		t.Fatalf("connect whose session already closed must fail")
	}
	if got := rt.ListSessions(); len(got) != 0 {
		t.Fatalf("phantom session listed after a pre-insert close: %+v", got)
	}
	closed, _, _ := m.fakeManager.snapshot()
	if len(closed) != 1 {
		t.Fatalf("manager disconnects = %v, want the dead session disconnected", closed)
	}
}

// --- I1: locally closed sessions must never grow the tombstone map ---

// tombstoneCount reads the tombstone map under mu (test-only accessor).
func (r *Runtime) tombstoneCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.tombstones)
}

// localClosingCount reads the localClosing set under mu (test-only accessor).
func (r *Runtime) localClosingCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.localClosing)
}

// TestLocalDisconnectNoTombstone (I1 RED): a local disconnect whose manager
// emits session:closed synchronously must not leave a tombstone. Session ids
// are never reused, so an implementation that tombstoned every locally closed
// session grew its map without bound.
func TestLocalDisconnectNoTombstone(t *testing.T) {
	const n = 100
	m := &emittingManager{fakeManager: newFakeManager(), emitOnDisconnect: true}
	rt := newTestRuntime(n, time.Minute, m, &fakeSFTP{}, nil)
	m.sink = rt.Sink()

	for i := 0; i < n; i++ {
		sid := connectOK(t, rt, "h1")
		rt.DisconnectSession(sid)
	}
	if got := rt.tombstoneCount(); got != 0 {
		t.Fatalf("tombstones = %d after %d local disconnect cycles, want 0", got, n)
	}
	if got := rt.localClosingCount(); got != 0 {
		t.Fatalf("localClosing = %d after %d local disconnect cycles, want 0 (mark cleanup leak)", got, n)
	}
}

// TestReapDisposeAllNoTombstone (I1 RED): Reap and DisposeAll tear sessions
// down through the same local-close path as DisconnectSession; none of them
// may leave tombstones behind.
func TestReapDisposeAllNoTombstone(t *testing.T) {
	m := &emittingManager{fakeManager: newFakeManager(), emitOnDisconnect: true}
	clk := newSettableClock()
	rt := newTestRuntime(8, time.Minute, m, &fakeSFTP{}, clk.now)
	m.sink = rt.Sink()

	for i := 0; i < 4; i++ {
		connectOK(t, rt, "h1")
	}
	clk.advance(2 * time.Minute)
	if closed := rt.Reap(clk.now()); len(closed) != 4 {
		t.Fatalf("reap closed %d sessions, want 4", len(closed))
	}
	if got := rt.tombstoneCount(); got != 0 {
		t.Fatalf("tombstones after reap = %d, want 0", got)
	}
	connectOK(t, rt, "h1")
	rt.DisposeAll()
	if got := rt.tombstoneCount(); got != 0 {
		t.Fatalf("tombstones after DisposeAll = %d, want 0", got)
	}
	if got := rt.localClosingCount(); got != 0 {
		t.Fatalf("localClosing after DisposeAll = %d, want 0 (mark cleanup leak)", got)
	}
}

// TestDiscardLateConnectNoTombstone (I1 RED): tearing down a session whose
// metadata was never inserted (a pre-insert close) calls manager.Disconnect,
// whose synchronous session:closed must not be tombstoned either — the
// discard path consumes the original tombstone but must not mint a new one.
func TestDiscardLateConnectNoTombstone(t *testing.T) {
	m := &emittingManager{fakeManager: newFakeManager(), emitOnConnect: true, emitOnDisconnect: true}
	rt := newTestRuntime(2, time.Minute, m, &fakeSFTP{}, nil)
	m.sink = rt.Sink()

	if _, err := rt.ConnectHost(context.Background(), "h1", sessions.ConnectOptions{}); err == nil {
		t.Fatal("connect whose session already closed must fail")
	}
	if got := rt.tombstoneCount(); got != 0 {
		t.Fatalf("tombstones after a pre-insert-close connect = %d, want 0", got)
	}
	if got := rt.localClosingCount(); got != 0 {
		t.Fatalf("localClosing after a pre-insert-close connect = %d, want 0 (mark cleanup leak)", got)
	}
}

// TestSftpDownloadGuardBeforeMkdir (F4 RED): SftpDownload must not create
// any local directory: a target whose parent is missing must error and leave
// no side effect, exactly like the Node guard (assertLocalPathUnderHome
// rejects a missing parent before any mkdir).
func TestSftpDownloadGuardBeforeMkdir(t *testing.T) {
	home := t.TempDir()
	svc := sftpservice.New(sftpservice.Deps{Opener: f4Opener{}, Home: home})
	m := newFakeManager()
	rt := newTestRuntime(4, time.Minute, m, svc, nil)
	sid := connectOK(t, rt, "h1")

	// Case A: parent outside home and absent — must error, parent uncreated.
	outside := filepath.Join(t.TempDir(), "absent-dir")
	outsideTarget := filepath.Join(outside, "file.txt")
	if _, err := rt.SftpDownload(context.Background(), sid, "/remote.txt", outsideTarget); err == nil {
		t.Fatalf("download into an absent out-of-home parent must fail")
	}
	if _, err := os.Stat(outside); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("out-of-home parent was created as a side effect: %v", err)
	}

	// Case B: parent inside home but absent — must error, parent uncreated.
	inside := filepath.Join(home, "absent-dir")
	insideTarget := filepath.Join(inside, "file.txt")
	if _, err := rt.SftpDownload(context.Background(), sid, "/remote.txt", insideTarget); err == nil {
		t.Fatalf("download into an absent in-home parent must fail (Node parity)")
	}
	if _, err := os.Stat(inside); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("in-home parent was created as a side effect: %v", err)
	}
}

// TestGuestSessionsBeginAllowsUnregisteredId: GuestSessions lets RunCommand
// proceed on a session the runtime never ConnectHost'd; the manager then
// accepts or rejects the id. Without the flag, begin still returns
// SESSION_NOT_FOUND before any Exec.
func TestGuestSessionsBeginAllowsUnregisteredId(t *testing.T) {
	m := newFakeManager()
	m.live["gui-1"] = true
	m.execOut = "up 4 days"
	rt := New(Deps{
		Hosts: newFakeHostStore(testHost("h1", "lab")), Manager: m, SFTP: &fakeSFTP{},
		MaxSessions: 2, IdleTimeout: time.Minute, GuestSessions: true,
	})

	out, err := rt.RunCommand(context.Background(), "gui-1", "uptime", 0)
	if err != nil {
		t.Fatalf("guest RunCommand: %v", err)
	}
	if out != "up 4 days" {
		t.Fatalf("output = %q", out)
	}
	if len(m.execSessions) != 1 || m.execSessions[0] != "gui-1" {
		t.Fatalf("exec sessions = %v, want [gui-1]", m.execSessions)
	}

	locked := newTestRuntime(2, time.Minute, newFakeManager(), &fakeSFTP{}, nil)
	_, err = locked.RunCommand(context.Background(), "gui-1", "uptime", 0)
	assertErrorCode(t, err, apperror.SessionNotFound)
}

// TestGuestSessionsRefuseAfterDisposeAll: a closed guest runtime must not
// run tools against leftover GUI session ids.
func TestGuestSessionsRefuseAfterDisposeAll(t *testing.T) {
	m := newFakeManager()
	m.live["gui-1"] = true
	rt := New(Deps{
		Hosts: newFakeHostStore(testHost("h1", "lab")), Manager: m, SFTP: &fakeSFTP{},
		GuestSessions: true,
	})
	rt.DisposeAll()
	_, err := rt.RunCommand(context.Background(), "gui-1", "uptime", 0)
	assertErrorCode(t, err, apperror.SessionNotFound)
}

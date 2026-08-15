package mcpcli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"nodeshell/internal/apperror"
	"nodeshell/internal/hosts"
	"nodeshell/internal/sessions"
	"nodeshell/internal/sftpservice"
	"nodeshell/internal/sshclient"
)

// assertErrorCode asserts err carries a stable code (any ErrorCode()
// implementation) equal to want.
func assertErrorCode(t *testing.T, err error, want string) {
	t.Helper()
	if err == nil {
		t.Fatalf("expected an error, got nil (want code %s)", want)
	}
	var coded interface{ ErrorCode() string }
	if !errors.As(err, &coded) {
		t.Fatalf("error %v does not carry a stable code (want %s)", err, want)
	}
	if coded.ErrorCode() != want {
		t.Fatalf("error code = %s, want %s (error: %v)", coded.ErrorCode(), want, err)
	}
}

// waitChan waits for a signal on ch or fails the test after 5s. Unbounded
// receives hang the whole package until CI's per-package timeout.
func waitChan(t *testing.T, ch <-chan struct{}, what string) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(5 * time.Second):
		t.Fatalf("timeout waiting for %s", what)
	}
}

// --- fake host store ---

type fakeHostStore struct {
	mu    sync.Mutex
	hosts []hosts.HostConfig
	err   error
}

func newFakeHostStore(hosts ...hosts.HostConfig) *fakeHostStore {
	return &fakeHostStore{hosts: hosts}
}

func (s *fakeHostStore) List() ([]hosts.HostConfig, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.err != nil {
		return nil, s.err
	}
	out := make([]hosts.HostConfig, len(s.hosts))
	copy(out, s.hosts)
	return out, nil
}

func (s *fakeHostStore) GetByID(id string) (hosts.HostConfig, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.err != nil {
		return hosts.HostConfig{}, false, s.err
	}
	for _, h := range s.hosts {
		if h.Id == id {
			return h, true, nil
		}
	}
	return hosts.HostConfig{}, false, nil
}

func testHost(id, name string) hosts.HostConfig {
	return hosts.HostConfig{
		Id: id, Name: name, Host: "192.0.2.10", Port: 22,
		Username: "user", AuthMethod: "password", CredentialsSaved: true,
	}
}

// --- fake session manager ---

type fakeManager struct {
	mu           sync.Mutex
	live         map[string]bool
	closed       []string
	nextID       int
	connectErr   error
	connectOpts  sessions.ConnectOptions
	connectHost  string
	execOut      string
	execErr      error
	execTimeouts []time.Duration
	execCmds     []string
	execSessions []string
	sftpClient   sshclient.SFTPClient
	sftpErr      error

	// connectStart / connectBlock make Connect block until connectBlock is
	// closed (used to hold connects in-flight for reserve tests); a cancelled
	// ctx releases the block too, like the production sshclient.Connect.
	connectStart chan struct{}
	connectBlock chan struct{}
	// connectReleased makes CancelConnect idempotent: the block channel is
	// closed exactly once.
	connectReleased bool
	// execStart / execBlock make Exec block until execBlock is closed (used
	// to hold a session busy for reap tests).
	execStart chan struct{}
	execBlock chan struct{}
	// disconnectStart / disconnectBlock make Disconnect signal and then
	// block until disconnectBlock is closed (used to freeze the reaper
	// mid-teardown for reap races).
	disconnectStart chan struct{}
	disconnectBlock chan struct{}
	// Manager shutdown recording (T1.7.4): CancelConnect/DisposeAll mark
	// their calls; CancelConnect also releases a blocked connect.
	cancelConnectCalled bool
	disposeAllCalled    bool
}

func newFakeManager() *fakeManager {
	return &fakeManager{live: map[string]bool{}}
}

func (m *fakeManager) Connect(ctx context.Context, hostID string, opts sessions.ConnectOptions) (sessions.ConnectResult, error) {
	if m.connectStart != nil {
		select {
		case m.connectStart <- struct{}{}:
		default:
		}
	}
	if m.connectBlock != nil {
		select {
		case <-m.connectBlock:
		case <-ctx.Done():
			return sessions.ConnectResult{}, &sessions.Error{Code: apperror.Cancelled, Message: "Connection cancelled"}
		}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.connectHost = hostID
	m.connectOpts = opts
	if m.connectErr != nil {
		return sessions.ConnectResult{}, m.connectErr
	}
	m.nextID++
	id := fmt.Sprintf("s%d", m.nextID)
	m.live[id] = true
	return sessions.ConnectResult{SessionID: id}, nil
}

// CancelConnect marks the call and releases every blocked connect (like the
// production manager cancelling the in-flight connect contexts). Idempotent.
func (m *fakeManager) CancelConnect() {
	m.mu.Lock()
	m.cancelConnectCalled = true
	ch := m.connectBlock
	released := m.connectReleased
	if ch != nil && !released {
		m.connectReleased = true
	}
	m.mu.Unlock()
	if ch != nil && !released {
		close(ch)
	}
}

// DisposeAll marks the call and quietly closes every live session, without
// emitting events (production parity: disposeQuiet). The runtime's own
// DisposeAll calls Disconnect afterwards; on the disposed manager every
// session is already gone, so those are no-ops.
func (m *fakeManager) DisposeAll() {
	m.CancelConnect()
	m.mu.Lock()
	m.disposeAllCalled = true
	for id := range m.live {
		delete(m.live, id)
		m.closed = append(m.closed, id)
	}
	m.mu.Unlock()
}

func (m *fakeManager) Disconnect(sessionID string) error {
	if m.disconnectStart != nil {
		select {
		case m.disconnectStart <- struct{}{}:
		default:
		}
	}
	if m.disconnectBlock != nil {
		<-m.disconnectBlock
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.live[sessionID] {
		delete(m.live, sessionID)
		m.closed = append(m.closed, sessionID)
	}
	return nil
}

func (m *fakeManager) Exec(sessionID string, ctx context.Context, command string, timeout time.Duration) (string, error) {
	if m.execStart != nil {
		select {
		case m.execStart <- struct{}{}:
		default:
		}
	}
	if m.execBlock != nil {
		select {
		case <-m.execBlock:
		case <-ctx.Done():
			return "", &sessions.Error{Code: apperror.Cancelled, Message: "Operation cancelled"}
		}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.execSessions = append(m.execSessions, sessionID)
	m.execTimeouts = append(m.execTimeouts, timeout)
	m.execCmds = append(m.execCmds, command)
	if !m.live[sessionID] {
		return "", &sessions.Error{Code: apperror.SessionNotFound, Message: "Session not found: " + sessionID}
	}
	if m.execErr != nil {
		return "", m.execErr
	}
	return m.execOut, nil
}

func (m *fakeManager) NewSFTPClient(sessionID string) (sshclient.SFTPClient, error) {
	return m.sftpClient, m.sftpErr
}

func (m *fakeManager) snapshot() (closed []string, opts sessions.ConnectOptions, timeouts []time.Duration) {
	m.mu.Lock()
	defer m.mu.Unlock()
	closed = append(closed, m.closed...)
	opts = m.connectOpts
	timeouts = append(timeouts, m.execTimeouts...)
	return
}

// --- fake SFTP ---

type sftpWriteCall struct{ sessionID, path, content string }
type sftpUploadCall struct{ sessionID, localPath, remoteName string }
type sftpDownloadCall struct{ sessionID, remotePath, localPath string }

type fakeSFTP struct {
	mu            sync.Mutex
	cwd           string
	chdirs        []string
	lists         []string
	entries       []sftpservice.Entry
	listErr       error
	readResolved  string
	readContent   string
	readErr       error
	writes        []sftpWriteCall
	writeResolved string
	writeErr      error
	uploads       []sftpUploadCall
	uploadErr     error
	downloads     []sftpDownloadCall
	downloadErr   error
	disposed      []string
	interrupted   []string
}

func (s *fakeSFTP) Chdir(sessionID, remotePath string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.chdirs = append(s.chdirs, remotePath)
	return s.cwd, nil
}

func (s *fakeSFTP) Cwd(sessionID string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.cwd, nil
}

func (s *fakeSFTP) List(sessionID, remotePath string) ([]sftpservice.Entry, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.lists = append(s.lists, remotePath)
	return s.entries, s.listErr
}

func (s *fakeSFTP) ReadText(sessionID, remotePath string, maxBytes int64) (string, string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.readResolved, s.readContent, s.readErr
}

func (s *fakeSFTP) WriteText(sessionID, remotePath, content string, maxBytes int64) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.writes = append(s.writes, sftpWriteCall{sessionID, remotePath, content})
	if s.writeErr != nil {
		return "", s.writeErr
	}
	if s.writeResolved != "" {
		return s.writeResolved, nil
	}
	return "/resolved" + remotePath, nil
}

func (s *fakeSFTP) UploadAs(sessionID, localPath, remoteName string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.uploads = append(s.uploads, sftpUploadCall{sessionID, localPath, remoteName})
	return s.uploadErr
}

func (s *fakeSFTP) Download(sessionID, remotePath, localPath string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.downloads = append(s.downloads, sftpDownloadCall{sessionID, remotePath, localPath})
	return s.downloadErr
}

func (s *fakeSFTP) Dispose(sessionID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.disposed = append(s.disposed, sessionID)
}

func (s *fakeSFTP) Interrupt(sessionID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.interrupted = append(s.interrupted, sessionID)
}

func (s *fakeSFTP) snapshot() (chdirs []string, writes []sftpWriteCall, uploads []sftpUploadCall, downloads []sftpDownloadCall, disposed []string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	chdirs = append(chdirs, s.chdirs...)
	writes = append(writes, s.writes...)
	uploads = append(uploads, s.uploads...)
	downloads = append(downloads, s.downloads...)
	disposed = append(disposed, s.disposed...)
	return
}

// --- settable clock ---

type settableClock struct {
	mu sync.Mutex
	t  time.Time
}

func newSettableClock() *settableClock {
	return &settableClock{t: time.Now()}
}

func (c *settableClock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *settableClock) advance(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

// --- runtime construction ---

// newTestRuntime wires a Runtime with the given deps; nil hosts/fakes are
// replaced by defaults (one known host, empty manager, empty sftp).
func newTestRuntime(max int, idle time.Duration, m SessionManager, s SFTP, clk func() time.Time) *Runtime {
	h := newFakeHostStore(testHost("h1", "lab"))
	if max <= 0 {
		max = 2
	}
	if idle <= 0 {
		idle = time.Minute
	}
	return New(Deps{Hosts: h, Manager: m, SFTP: s, MaxSessions: max, IdleTimeout: idle, Clock: clk})
}

func connectOK(t *testing.T, rt *Runtime, hostID string) string {
	t.Helper()
	res, err := rt.ConnectHost(context.Background(), hostID, sessions.ConnectOptions{})
	if err != nil {
		t.Fatalf("ConnectHost(%s): %v", hostID, err)
	}
	if res.SessionID == "" {
		t.Fatal("ConnectHost returned an empty session id")
	}
	return res.SessionID
}

// connectEmittingManager wraps a fakeManager and emits a session:closed event
// for a freshly connected session through the given sink before returning —
// the scenario where the manager's close event beats the runtime's metadata
// insert.
type connectEmittingManager struct {
	*fakeManager
	sink sessions.EventSink
}

func (m *connectEmittingManager) Connect(ctx context.Context, hostID string, opts sessions.ConnectOptions) (sessions.ConnectResult, error) {
	res, err := m.fakeManager.Connect(ctx, hostID, opts)
	if err == nil {
		m.sink.Emit(sessions.EventSessionClosed, sessions.ClosedEvent{SessionID: res.SessionID})
	}
	return res, err
}

// emittingManager wraps a fakeManager and optionally emits a session:closed
// event through the runtime sink synchronously inside Connect (a pre-insert
// close) and/or Disconnect (a local close), like the production manager. The
// runtime must tolerate these events without growing tombstones for locally
// closed sessions.
type emittingManager struct {
	*fakeManager
	sink             sessions.EventSink
	emitOnConnect    bool
	emitOnDisconnect bool
}

func (m *emittingManager) Connect(ctx context.Context, hostID string, opts sessions.ConnectOptions) (sessions.ConnectResult, error) {
	res, err := m.fakeManager.Connect(ctx, hostID, opts)
	if err == nil && m.emitOnConnect {
		m.sink.Emit(sessions.EventSessionClosed, sessions.ClosedEvent{SessionID: res.SessionID})
	}
	return res, err
}

func (m *emittingManager) Disconnect(sessionID string) error {
	err := m.fakeManager.Disconnect(sessionID)
	if err == nil && m.emitOnDisconnect {
		m.sink.Emit(sessions.EventSessionClosed, sessions.ClosedEvent{SessionID: sessionID})
	}
	return err
}

// --- minimal real-SFTP fake for the download guard test ---

// f4SFTPClient satisfies sshclient.SFTPClient for a single regular remote
// file whose stream is empty.
type f4SFTPClient struct{}

func (f4SFTPClient) ReadDir(string) ([]os.FileInfo, error) { return nil, nil }
func (f4SFTPClient) Stat(string) (os.FileInfo, error)      { return f4FileInfo{}, nil }
func (f4SFTPClient) Lstat(string) (os.FileInfo, error)     { return f4FileInfo{}, nil }
func (f4SFTPClient) RealPath(string) (string, error)       { return "/remote.txt", nil }
func (f4SFTPClient) Open(string) (io.ReadCloser, error) {
	return io.NopCloser(strings.NewReader("")), nil
}
func (f4SFTPClient) Create(string) (io.WriteCloser, error) { return nopWriteCloser{}, nil }
func (f4SFTPClient) Mkdir(string) error                    { return nil }
func (f4SFTPClient) MkdirAll(string) error                 { return nil }
func (f4SFTPClient) Remove(string) error                   { return nil }
func (f4SFTPClient) RemoveDirectory(string) error          { return nil }
func (f4SFTPClient) Rename(string, string) error           { return nil }
func (f4SFTPClient) PosixRename(string, string) error      { return nil }
func (f4SFTPClient) Chmod(string, os.FileMode) error       { return nil }
func (f4SFTPClient) HasExtension(string) (string, bool)    { return "", false }
func (f4SFTPClient) Close() error                          { return nil }

type f4FileInfo struct{}

func (f4FileInfo) Name() string       { return "remote.txt" }
func (f4FileInfo) Size() int64        { return 0 }
func (f4FileInfo) Mode() os.FileMode  { return 0o644 }
func (f4FileInfo) ModTime() time.Time { return time.Time{} }
func (f4FileInfo) IsDir() bool        { return false }
func (f4FileInfo) Sys() any           { return nil }

type nopWriteCloser struct{}

func (nopWriteCloser) Write(p []byte) (int, error) { return len(p), nil }
func (nopWriteCloser) Close() error                { return nil }

type f4Opener struct{}

func (f4Opener) NewSFTPClient(string) (sshclient.SFTPClient, error) { return f4SFTPClient{}, nil }

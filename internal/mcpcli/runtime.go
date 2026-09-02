// Package mcpcli owns the MCP runtime: the ten business tools over a host
// store, an SSH session manager and an SFTP service, plus the session policy
// (max sessions, idle reaping, busy protection). The stdio JSON-RPC transport
// (T1.7.4) wires this package; it never depends on Wails or the GUI session
// manager — the production wiring passes an independent sessions.Manager
// (with a nop sink) and an independent sftpservice.
package mcpcli

import (
	"context"
	"fmt"
	"sync"
	"time"

	"nodeshell/internal/apperror"
	"nodeshell/internal/hosts"
	"nodeshell/internal/permission"
	"nodeshell/internal/sessions"
	"nodeshell/internal/sftpservice"
	"nodeshell/internal/sshclient"
)

// Policy constants mirror the settings defaults and the Electron runtime.
const (
	// DefaultMaxSessions is the fallback when the wired policy is <= 0
	// (settings default: 8).
	DefaultMaxSessions = 8
	// DefaultIdleTimeout is the fallback idle reap timeout (settings default:
	// 10 minutes).
	DefaultIdleTimeout = 10 * time.Minute
	// DefaultReapInterval matches IDLE_CHECK_INTERVAL_MS in mcp-runtime.ts.
	DefaultReapInterval = 15 * time.Second
	// MaxFileBytes is the hard MCP text-file cap (MAX_MCP_FILE_BYTES).
	MaxFileBytes = 512 * 1024
	// MinCommandTimeoutMs / MaxCommandTimeoutMs are the run_command timeout
	// bounds of the tool schema; a timeout outside the range clamps, zero or
	// negative falls back to the 60s default.
	MinCommandTimeoutMs = 1
	MaxCommandTimeoutMs = 300000
	// DefaultCommandTimeoutMs matches the TS default.
	DefaultCommandTimeoutMs = 60000
)

// HostStore is the narrow host-config dependency (hosts.Store satisfies it).
type HostStore interface {
	List() ([]hosts.HostConfig, error)
	GetByID(id string) (hosts.HostConfig, bool, error)
}

// SessionManager is the narrow SSH dependency of the runtime
// (*sessions.Manager satisfies it). Tests inject a fake; production must use
// an independent manager, never the GUI's.
type SessionManager interface {
	Connect(ctx context.Context, hostID string, opts sessions.ConnectOptions) (sessions.ConnectResult, error)
	Disconnect(sessionID string) error
	Exec(sessionID string, ctx context.Context, command string, timeout time.Duration) (string, error)
	NewSFTPClient(sessionID string) (sshclient.SFTPClient, error)
}

// SFTP is the narrow file-operation dependency (*sftpservice.Service
// satisfies it).
type SFTP interface {
	Chdir(sessionID, remotePath string) (string, error)
	Cwd(sessionID string) (string, error)
	List(sessionID, remotePath string) ([]sftpservice.Entry, error)
	ReadText(sessionID, remotePath string, maxBytes int64) (string, string, error)
	WriteText(sessionID, remotePath, content string, maxBytes int64) (string, error)
	UploadAs(sessionID, localPath, remoteName string) error
	Download(sessionID, remotePath, localPath string) error
	Dispose(sessionID string)
	// Interrupt closes the cached client so an in-flight pkg/sftp call
	// unblocks, but keeps the session handle and cwd. Guest (in-app Agent)
	// cancel uses this so the GUI SFTP panel is not snapped back to home.
	Interrupt(sessionID string)
}

// Error carries the stable MCP error code for the server to format; messages
// never embed passwords, local paths or remote paths.
type Error struct {
	Code    string
	Message string
}

func (e *Error) Error() string { return e.Message }

func (e *Error) ErrorCode() string { return e.Code }

// Deps wires a Runtime. Manager/SFTP may be nil at construction and attached
// later via SetManager/SetSFTP (the production wiring creates the manager
// with the runtime's Sink, breaking the construction cycle).
type Deps struct {
	Hosts       HostStore
	Manager     SessionManager
	SFTP        SFTP
	MaxSessions int
	IdleTimeout time.Duration
	// Clock is the "now" seam for metadata timestamps and the reaper; nil
	// uses time.Now.
	Clock func() time.Time
	// NextSink receives forwarded session events after the runtime has
	// handled them (production passes nil/nop).
	NextSink sessions.EventSink
	// Auth is the user-consent gate for sensitive tools. Nil skips only that
	// prompt (MCP external mode and most tests). Path guards, host keys,
	// session limits and argument validation still run.
	Auth permission.Authorizer
	// GuestSessions lets Call operate on session ids the runtime did not
	// ConnectHost. The in-app Agent uses this so GUI tabs are valid targets.
	// Production --mcp wiring leaves it false. A guest runtime must not start
	// the idle reaper: there is no metadata to protect GUI sessions with.
	GuestSessions bool
}

// sessionMeta is one session's MCP-side state. Passwords never live here.
type sessionMeta struct {
	sessionID string
	hostID    string
	title     string
	lastUsed  time.Time
	// busyCount is the number of in-flight tool operations; the idle reaper
	// never closes a busy session (close-under-op protection).
	busyCount int
	// reaping is set under mu when the reaper selects the session; begin()
	// refuses a reaping session, so an operation can never start on a
	// session whose teardown has been decided (begin/reap linearisation).
	reaping bool
}

// Runtime owns the MCP business logic. All session metadata is protected by
// mu; network/Dispose calls never run under it (the manager's session:closed
// event is emitted synchronously and would deadlock a held lock).
type Runtime struct {
	hosts   HostStore
	manager SessionManager
	sftp    SFTP
	clock   func() time.Time
	sink    *runtimeSink
	maxSess int
	idleTmo time.Duration
	auth    permission.Authorizer
	guest   bool

	mu      sync.Mutex
	meta    map[string]*sessionMeta
	order   []string // insertion order for list_sessions (TS Map semantics)
	pending int      // in-flight connect reservations
	// closed is set by DisposeAll under mu; reserve() and the connect insert
	// refuse afterwards, so a session can never resurrect after shutdown.
	closed bool
	// tombstones records session ids whose session:closed arrived before the
	// metadata insert, so ConnectHost never registers a phantom session.
	tombstones map[string]bool
	// localClosing records session ids whose teardown was initiated locally
	// (disconnect/reap/DisposeAll/discardLateConnect). The manager's
	// synchronous session:closed for such a session must not be tombstoned;
	// the entry is removed when the event arrives or, if the manager never
	// emits one, right after the Disconnect call.
	localClosing map[string]bool

	reapMu sync.Mutex
	reap   *reaperGen
	// beforeReaperPublish, if non-nil, runs immediately before a reap-loop
	// generation is published. Test seam only — production never sets it.
	beforeReaperPublish func()
}

// reaperGen is one reap-loop generation. stop is closed by the single owner
// to stop the loop; the loop closes done on exit; wg joins it. Ownership is
// detached atomically under reapMu, so concurrent StopReaper callers can
// never double-close a channel.
type reaperGen struct {
	stop chan struct{}
	done chan struct{}
	wg   sync.WaitGroup
}

// New builds a Runtime. Zero/negative policy values fall back to the product
// defaults; the settings wiring normally passes already-normalized values.
func New(d Deps) *Runtime {
	maxSess, idle := clampPolicy(d.MaxSessions, d.IdleTimeout)
	clock := d.Clock
	if clock == nil {
		clock = time.Now
	}
	r := &Runtime{
		hosts:   d.Hosts,
		manager: d.Manager,
		sftp:    d.SFTP,
		clock:   clock,
		maxSess: maxSess,
		idleTmo: idle,
		auth:    d.Auth,
		guest:   d.GuestSessions,
		meta:    map[string]*sessionMeta{},
	}
	r.tombstones = map[string]bool{}
	r.localClosing = map[string]bool{}
	r.sink = &runtimeSink{r: r, next: d.NextSink}
	return r
}

// clampPolicy falls back to the defaults for non-positive values. The
// settings store has already normalized wired values, so this only guards
// against a missing wiring.
func clampPolicy(maxSessions int, idleTimeout time.Duration) (int, time.Duration) {
	if maxSessions <= 0 {
		maxSessions = DefaultMaxSessions
	}
	if idleTimeout <= 0 {
		idleTimeout = DefaultIdleTimeout
	}
	return maxSessions, idleTimeout
}

// Sink returns the EventSink to wire into the production sessions.Manager;
// the runtime handles session:closed (metadata + SFTP cleanup) and forwards
// everything to NextSink.
func (r *Runtime) Sink() sessions.EventSink { return r.sink }

// SetManager attaches the session manager after construction (production
// wiring: manager built with Sink(), then attached here).
func (r *Runtime) SetManager(m SessionManager) {
	r.manager = m
}

// SetSFTP attaches the SFTP service after construction.
func (r *Runtime) SetSFTP(s SFTP) {
	r.sftp = s
}

func (r *Runtime) sessionTitle(sessionID string) string {
	r.mu.Lock()
	defer r.mu.Unlock()
	if m := r.meta[sessionID]; m != nil {
		return m.title
	}
	return ""
}

// CallOpts customizes one Call. The zero value matches stdio MCP: SourceMCP
// and the title recorded at ConnectHost (empty for guest sessions).
type CallOpts struct {
	// Source labels the permission prompt. Empty uses SourceMCP.
	Source string
	// Title is shown in the permission prompt. Empty uses the runtime
	// metadata title.
	Title string
}

// authorize is the user-consent step before a sensitive tool runs. Nil Auth
// skips only this prompt (MCP external mode / tests) and must not be read as
// skipping path, session, host-key, or argument checks. The request never
// carries file contents or passwords.
func (r *Runtime) authorize(ctx context.Context, opts CallOpts, tool, sessionID, summary, detail string) error {
	if r.auth == nil {
		return nil
	}
	source := opts.Source
	if source == "" {
		source = permission.SourceMCP
	}
	title := opts.Title
	if title == "" {
		title = r.sessionTitle(sessionID)
	}
	return r.auth.Authorize(ctx, permission.Request{
		Source:    source,
		Tool:      tool,
		SessionID: sessionID,
		Title:     title,
		Summary:   permission.Truncate(summary),
		Detail:    detail,
	})
}

// runtimeSink forwards session events to the runtime's closed handler, then
// to the next sink.
type runtimeSink struct {
	r    *Runtime
	next sessions.EventSink
}

func (s *runtimeSink) Emit(event string, payload any) {
	if event == sessions.EventSessionClosed {
		if e, ok := payload.(sessions.ClosedEvent); ok {
			s.r.onClosed(e.SessionID)
		}
	}
	if s.next != nil {
		s.next.Emit(event, payload)
	}
}

// onClosed is the session:closed handler: a remote fault drops the metadata
// and disposes the session's SFTP handle. A close that beat the metadata
// insert is tombstoned so ConnectHost can never register a phantom session —
// unless the close was initiated locally, in which case the session is in
// localClosing and nothing is recorded (ids are never reused, so tombstones
// would grow without bound). Never called with mu held.
func (r *Runtime) onClosed(sessionID string) {
	r.mu.Lock()
	_, ok := r.meta[sessionID]
	closing := r.localClosing[sessionID]
	if closing {
		delete(r.localClosing, sessionID)
	}
	if ok {
		r.dropLocked(sessionID)
	} else if !closing {
		r.tombstones[sessionID] = true
	}
	r.mu.Unlock()
	if ok {
		r.sftpDispose(sessionID)
	}
	if f, ok := r.auth.(interface{ ForgetSession(string) }); ok {
		f.ForgetSession(sessionID)
	}
}

// ListHosts returns the saved hosts as the list_hosts DTO (no secrets).
func (r *Runtime) ListHosts() ([]HostDTO, error) {
	list, err := r.hosts.List()
	if err != nil {
		return nil, err
	}
	out := make([]HostDTO, 0, len(list))
	for _, h := range list {
		out = append(out, HostDTO{
			ID:               h.Id,
			Name:             h.Name,
			Host:             h.Host,
			Port:             h.Port,
			Username:         h.Username,
			AuthMethod:       h.AuthMethod,
			CredentialsSaved: h.CredentialsSaved,
		})
	}
	return out, nil
}

// ListSessions returns the session array in insertion order (TS Map
// semantics), deterministic under concurrency.
func (r *Runtime) ListSessions() []SessionDTO {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]SessionDTO, 0, len(r.order))
	for _, id := range r.order {
		m, ok := r.meta[id]
		if !ok {
			continue
		}
		out = append(out, SessionDTO{SessionID: m.sessionID, HostID: m.hostID, Title: m.title})
	}
	return out
}

// ConnectHost reserves a session slot, connects through the manager and
// records the metadata. The password/acceptHostKey options reach the manager
// verbatim (which resolves saved credentials itself); the runtime never reads
// or stores plaintext credentials.
func (r *Runtime) ConnectHost(ctx context.Context, hostID string, opts sessions.ConnectOptions) (ConnectResult, error) {
	if err := r.reserve(); err != nil {
		return ConnectResult{}, err
	}
	committed := false
	defer func() {
		if !committed {
			r.releaseReserve()
		}
	}()

	host, ok, err := r.hosts.GetByID(hostID)
	if err != nil {
		return ConnectResult{}, err
	}
	if !ok {
		return ConnectResult{}, &Error{Code: apperror.HostNotFound, Message: fmt.Sprintf("Host not found: %s", hostID)}
	}
	res, err := r.manager.Connect(ctx, hostID, opts)
	if err != nil {
		return ConnectResult{}, err
	}
	title := host.Username + "@" + host.Host
	now := r.clock()
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		r.discardLateConnect(res.SessionID)
		return ConnectResult{}, &Error{Code: apperror.Cancelled, Message: "Runtime is shut down"}
	}
	if r.tombstones[res.SessionID] {
		delete(r.tombstones, res.SessionID)
		r.mu.Unlock()
		r.discardLateConnect(res.SessionID)
		return ConnectResult{}, &Error{Code: apperror.Unknown, Message: "Connection closed during setup"}
	}
	r.meta[res.SessionID] = &sessionMeta{
		sessionID: res.SessionID,
		hostID:    hostID,
		title:     title,
		lastUsed:  now,
	}
	r.order = append(r.order, res.SessionID)
	r.pending--
	committed = true
	r.mu.Unlock()
	return ConnectResult{SessionID: res.SessionID, Title: title}, nil
}

// discardLateConnect tears down a session whose metadata was never inserted
// (runtime closed or closed during setup): the manager connection is closed
// and any SFTP handle disposed, never under mu. The session is marked as a
// locally closing session first so the synchronous session:closed emitted by
// Disconnect is not tombstoned (the metadata was never inserted and ids are
// never reused).
func (r *Runtime) discardLateConnect(sessionID string) {
	r.sftpDispose(sessionID)
	if r.manager == nil {
		return
	}
	r.mu.Lock()
	r.localClosing[sessionID] = true
	r.mu.Unlock()
	r.manager.Disconnect(sessionID)
	r.mu.Lock()
	delete(r.localClosing, sessionID)
	r.mu.Unlock()
}

// reserve atomically books one session slot; the limit counts both
// established sessions and in-flight connects, so concurrent connects can
// never overshoot. A closed runtime (DisposeAll) refuses every connect.
func (r *Runtime) reserve() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return &Error{Code: apperror.Cancelled, Message: "Runtime is shut down"}
	}
	if len(r.meta)+r.pending >= r.maxSess {
		return &Error{Code: apperror.McpSessionLimit, Message: fmt.Sprintf("Too many MCP sessions (max %d); disconnect one first", r.maxSess)}
	}
	r.pending++
	return nil
}

// releaseReserve frees a pending slot after a failed connect.
func (r *Runtime) releaseReserve() {
	r.mu.Lock()
	if r.pending > 0 {
		r.pending--
	}
	r.mu.Unlock()
}

// DisconnectSession ends the session; unknown ids are a silent no-op.
func (r *Runtime) DisconnectSession(sessionID string) {
	r.disconnectSession(sessionID)
}

// disconnectSession removes the metadata and tears the session down. The
// session is marked as a locally closing session before the manager call, so
// the manager's synchronous session:closed event is never tombstoned (ids are
// never reused); if the manager emits no event, the mark is cleaned up right
// after Disconnect returns. No lock is held across the manager call.
func (r *Runtime) disconnectSession(sessionID string) {
	r.mu.Lock()
	_, ok := r.meta[sessionID]
	if ok {
		r.dropLocked(sessionID)
		r.localClosing[sessionID] = true
	}
	r.mu.Unlock()
	if !ok {
		return
	}
	r.sftpDispose(sessionID)
	if r.manager != nil {
		r.manager.Disconnect(sessionID)
	}
	r.mu.Lock()
	delete(r.localClosing, sessionID)
	r.mu.Unlock()
}

// dropLocked removes the metadata. The caller must hold mu.
func (r *Runtime) dropLocked(sessionID string) {
	delete(r.meta, sessionID)
	for i, id := range r.order {
		if id == sessionID {
			r.order = append(r.order[:i], r.order[i+1:]...)
			return
		}
	}
}

// sftpDispose is safe to call with a nil service.
func (r *Runtime) sftpDispose(sessionID string) {
	if r.sftp != nil {
		r.sftp.Dispose(sessionID)
	}
}

// sftpInterrupt is safe to call with a nil service.
func (r *Runtime) sftpInterrupt(sessionID string) {
	if r.sftp != nil {
		r.sftp.Interrupt(sessionID)
	}
}

// RunCommand runs a remote command over the session. The timeout clamps into
// [1, 300000] ms with a 60s default; the 2MiB stdout cap lives in the
// sshclient exec layer. The session is marked busy for the whole call so the
// reaper never closes it mid-command.
func (r *Runtime) RunCommand(ctx context.Context, sessionID, command string, timeoutMs int64) (string, error) {
	done, err := r.begin(sessionID)
	if err != nil {
		return "", err
	}
	defer done()
	if r.manager == nil {
		return "", &Error{Code: apperror.Unknown, Message: "Session manager is not initialised"}
	}
	return r.manager.Exec(sessionID, ctx, command, clampTimeout(timeoutMs))
}

// clampTimeout applies the run_command schema bounds: zero/negative falls
// back to the 60s default, out-of-range clamps to [1, 300000] ms.
func clampTimeout(ms int64) time.Duration {
	if ms <= 0 {
		ms = DefaultCommandTimeoutMs
	}
	if ms < MinCommandTimeoutMs {
		ms = MinCommandTimeoutMs
	}
	if ms > MaxCommandTimeoutMs {
		ms = MaxCommandTimeoutMs
	}
	return time.Duration(ms) * time.Millisecond
}

// sftpResult is one sftpOp worker outcome.
type sftpResult[T any] struct {
	value T
	err   error
}

// sftpOp runs one SFTP operation under ctx. pkg/sftp has no context API, so
// the operation runs on a worker goroutine; a cancelled ctx closes the
// session's SFTP client — unblocking every in-flight pkg/sftp call — then
// joins the worker (never a goroutine leak) and maps the outcome to
// CANCELLED. MCP-owned sessions Dispose the handle; guest sessions Interrupt
// so the GUI panel's cwd survives. The operation's own result wins when it
// finishes before the cancel is observed. The caller holds the session busy
// via begin/done; the join happens before this returns, so the busy count is
// never released while the worker could still be running. All five SFTP
// tools route through this helper; the ctx-aware operations (run_command,
// connect) pass ctx into the manager directly.
func sftpOp[T any](r *Runtime, ctx context.Context, sessionID string, run func() (T, error)) (T, error) {
	ch := make(chan sftpResult[T], 1)
	go func() {
		v, err := run()
		ch <- sftpResult[T]{value: v, err: err}
	}()
	select {
	case res := <-ch:
		return res.value, res.err
	case <-ctx.Done():
		// pkg/sftp has no context API: closing the client unblocks the
		// worker. MCP-owned sessions Dispose the whole handle. Guest
		// sessions share the GUI SFTP service, so cancel only Interrupts
		// the cached client and keeps Session.cwd for the panel.
		if r.guest {
			r.sftpInterrupt(sessionID)
		} else {
			r.sftpDispose(sessionID)
		}
		<-ch // join the worker so no goroutine outlives the call
		var zero T
		return zero, &Error{Code: apperror.Cancelled, Message: "Operation cancelled"}
	}
}

// SftpList lists the session's remote directory; an optional path is chdir'd
// first (the TS side effect).
func (r *Runtime) SftpList(ctx context.Context, sessionID, remotePath string) (SftpListResult, error) {
	return r.sftpList(ctx, sessionID, remotePath, true)
}

// sftpList lists a remote directory. When chdir is true (MCP default) an
// optional path is chdir'd first so later tools see that cwd. When chdir is
// false the path is listed in place and the session cwd is left alone — the
// in-app Agent uses that so the GUI SFTP panel does not jump.
func (r *Runtime) sftpList(ctx context.Context, sessionID, remotePath string, chdir bool) (SftpListResult, error) {
	done, err := r.begin(sessionID)
	if err != nil {
		return SftpListResult{}, err
	}
	defer done()
	if r.sftp == nil {
		return SftpListResult{}, &Error{Code: apperror.Unknown, Message: "SFTP is not initialised"}
	}
	return sftpOp(r, ctx, sessionID, func() (SftpListResult, error) {
		listPath := remotePath
		if chdir {
			if remotePath != "" {
				if _, err := r.sftp.Chdir(sessionID, remotePath); err != nil {
					return SftpListResult{}, err
				}
			}
			listPath = ""
		}
		cwd, err := r.sftp.Cwd(sessionID)
		if err != nil {
			return SftpListResult{}, err
		}
		entries, err := r.sftp.List(sessionID, listPath)
		if err != nil {
			return SftpListResult{}, err
		}
		return SftpListResult{Cwd: cwd, Entries: entries}, nil
	})
}

// SftpRead reads a remote text file with the 512KiB cap; the result carries
// the resolved remote path.
func (r *Runtime) SftpRead(ctx context.Context, sessionID, remotePath string) (SftpReadResult, error) {
	done, err := r.begin(sessionID)
	if err != nil {
		return SftpReadResult{}, err
	}
	defer done()
	if r.sftp == nil {
		return SftpReadResult{}, &Error{Code: apperror.Unknown, Message: "SFTP is not initialised"}
	}
	return sftpOp(r, ctx, sessionID, func() (SftpReadResult, error) {
		resolved, content, err := r.sftp.ReadText(sessionID, remotePath, MaxFileBytes)
		if err != nil {
			return SftpReadResult{}, err
		}
		return SftpReadResult{Path: resolved, Content: content}, nil
	})
}

// SftpWrite writes UTF-8 text (512KiB cap) and returns the resolved remote
// path of the written target.
func (r *Runtime) SftpWrite(ctx context.Context, sessionID, remotePath, content string) (SftpWriteResult, error) {
	done, err := r.begin(sessionID)
	if err != nil {
		return SftpWriteResult{}, err
	}
	defer done()
	if r.sftp == nil {
		return SftpWriteResult{}, &Error{Code: apperror.Unknown, Message: "SFTP is not initialised"}
	}
	return sftpOp(r, ctx, sessionID, func() (SftpWriteResult, error) {
		resolved, err := r.sftp.WriteText(sessionID, remotePath, content, MaxFileBytes)
		if err != nil {
			return SftpWriteResult{}, err
		}
		return SftpWriteResult{OK: true, Path: resolved}, nil
	})
}

// SftpUpload uploads a home-bound local file into the session's current
// remote directory, optionally under an explicit remote name (the service
// enforces the home boundary and the atomic commit).
func (r *Runtime) SftpUpload(ctx context.Context, sessionID, localPath, remoteName string) (SftpUploadResult, error) {
	done, err := r.begin(sessionID)
	if err != nil {
		return SftpUploadResult{}, err
	}
	defer done()
	if r.sftp == nil {
		return SftpUploadResult{}, &Error{Code: apperror.Unknown, Message: "SFTP is not initialised"}
	}
	return sftpOp(r, ctx, sessionID, func() (SftpUploadResult, error) {
		if err := r.sftp.UploadAs(sessionID, localPath, remoteName); err != nil {
			return SftpUploadResult{}, err
		}
		var name *string
		if remoteName != "" {
			n := remoteName
			name = &n
		}
		return SftpUploadResult{OK: true, LocalPath: localPath, RemoteName: name}, nil
	})
}

// SftpDownload downloads a remote file to a home-bound local path. The
// service resolves the target against the home boundary before any local I/O
// and requires the parent directory to already exist — no directory is ever
// created here (Node parity: assertLocalPathUnderHome rejects a missing
// parent before the download's mkdir), so a guard failure leaves no side
// effect.
func (r *Runtime) SftpDownload(ctx context.Context, sessionID, remotePath, localPath string) (SftpDownloadResult, error) {
	done, err := r.begin(sessionID)
	if err != nil {
		return SftpDownloadResult{}, err
	}
	defer done()
	if r.sftp == nil {
		return SftpDownloadResult{}, &Error{Code: apperror.Unknown, Message: "SFTP is not initialised"}
	}
	return sftpOp(r, ctx, sessionID, func() (SftpDownloadResult, error) {
		if err := r.sftp.Download(sessionID, remotePath, localPath); err != nil {
			return SftpDownloadResult{}, err
		}
		return SftpDownloadResult{OK: true, RemotePath: remotePath, LocalPath: localPath}, nil
	})
}

// begin marks a session busy for one tool operation and refreshes lastUsed
// (operation start). The returned function ends the operation (busy -1,
// lastUsed refreshed again). A missing session — or one the reaper has
// already selected — is SESSION_NOT_FOUND, so an operation can never start
// on a session whose teardown has been decided.
func (r *Runtime) begin(sessionID string) (func(), error) {
	r.mu.Lock()
	m, ok := r.meta[sessionID]
	if !ok {
		guest := r.guest && !r.closed
		r.mu.Unlock()
		if guest {
			return func() {}, nil
		}
		return nil, &Error{Code: apperror.SessionNotFound, Message: fmt.Sprintf("Session not found: %s", sessionID)}
	}
	if m.reaping {
		r.mu.Unlock()
		return nil, &Error{Code: apperror.SessionNotFound, Message: fmt.Sprintf("Session not found: %s", sessionID)}
	}
	m.busyCount++
	m.lastUsed = r.clock()
	r.mu.Unlock()
	return func() {
		r.mu.Lock()
		if cur, ok := r.meta[sessionID]; ok {
			cur.busyCount--
			cur.lastUsed = r.clock()
		}
		r.mu.Unlock()
	}, nil
}

// Reap closes every session idle for at least the idle timeout with no
// in-flight operation, returning the closed ids in insertion order. now is
// injectable for tests. Candidates are marked reaping under the lock before
// any teardown: a begin() after the mark is refused, so a busy session is
// never torn down (busy sessions are never marked) and a session is never
// torn down under an operation that starts after the reap decision.
func (r *Runtime) Reap(now time.Time) []string {
	r.mu.Lock()
	var ids []string
	for _, id := range r.order {
		m, ok := r.meta[id]
		if !ok || m.busyCount > 0 || m.reaping {
			continue
		}
		if now.Sub(m.lastUsed) >= r.idleTmo {
			m.reaping = true
			ids = append(ids, id)
		}
	}
	r.mu.Unlock()
	for _, id := range ids {
		r.disconnectSession(id)
	}
	return ids
}

// StartReaper spawns the idle-reap loop at the given interval (default 15s).
// Idempotent: a running loop is never replaced, and a closed runtime (after
// DisposeAll) refuses to start one. The closed check and the generation
// publish both run under reapMu (reapMu before mu), so a DisposeAll cannot
// slip between them: either it sets closed before the publish (StartReaper
// then refuses) or it publishes first (DisposeAll then detaches and joins the
// loop). No reap loop can survive a closed runtime.
func (r *Runtime) StartReaper(interval time.Duration) {
	if interval <= 0 {
		interval = DefaultReapInterval
	}
	r.reapMu.Lock()
	defer r.reapMu.Unlock()
	if r.reap != nil {
		return
	}
	r.mu.Lock()
	closed := r.closed
	r.mu.Unlock()
	if closed {
		return
	}
	if r.beforeReaperPublish != nil {
		r.beforeReaperPublish()
	}
	gen := &reaperGen{stop: make(chan struct{}), done: make(chan struct{})}
	gen.wg.Add(1)
	r.reap = gen
	go func() {
		defer gen.wg.Done()
		defer close(gen.done)
		t := time.NewTicker(interval)
		defer t.Stop()
		for {
			select {
			case <-t.C:
				r.Reap(r.clock())
			case <-gen.stop:
				return
			}
		}
	}()
}

// StopReaper stops and joins the reap loop. Idempotent and safe to call
// concurrently: ownership of the generation (stop/done/wg) is detached
// atomically under reapMu, so exactly one caller closes the stop channel and
// waits; every other caller sees nothing to stop and returns.
func (r *Runtime) StopReaper() {
	r.reapMu.Lock()
	gen := r.reap
	r.reap = nil
	r.reapMu.Unlock()
	if gen == nil {
		return
	}
	close(gen.stop)
	<-gen.done
	gen.wg.Wait()
}

// DisposeAll closes the runtime, stops and joins the reaper, then disconnects
// every session and disposes its SFTP handle. Idempotent and safe to call
// concurrently. The closed gate is set before the snapshot, so an in-flight
// connect that completes afterwards is torn down instead of resurrecting a
// session. The generation detach runs under reapMu (reapMu before mu, same
// order as StartReaper), so a concurrent StartReaper either sees closed and
// refuses to publish or publishes first and is detached and joined here — the
// loop can never be left running after DisposeAll returns.
func (r *Runtime) DisposeAll() {
	r.reapMu.Lock()
	r.mu.Lock()
	r.closed = true
	ids := make([]string, 0, len(r.order))
	ids = append(ids, r.order...)
	r.mu.Unlock()
	gen := r.reap
	r.reap = nil
	r.reapMu.Unlock()
	if gen != nil {
		close(gen.stop)
		<-gen.done
		gen.wg.Wait()
	}
	for _, id := range ids {
		r.disconnectSession(id)
	}
}

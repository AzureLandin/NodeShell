package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path"
	"strings"
	"sync"

	"github.com/google/uuid"
	"github.com/wailsapp/wails/v2/pkg/runtime"

	"nodeshell/internal/agent"
	"nodeshell/internal/apperror"
	"nodeshell/internal/configdir"
	"nodeshell/internal/credentials"
	"nodeshell/internal/credentials/keyring"
	"nodeshell/internal/fonts"
	"nodeshell/internal/hosts"
	"nodeshell/internal/knownhosts"
	"nodeshell/internal/mcpcli"
	"nodeshell/internal/mcpregistration"
	"nodeshell/internal/monitor"
	"nodeshell/internal/permission"
	"nodeshell/internal/sessions"
	"nodeshell/internal/settings"
	"nodeshell/internal/sftpservice"
	"nodeshell/internal/tunnel"
)

// errBackendNotInitialised is returned by bound methods until startup has
// wired the domain services, so the frontend never sees a fake success.
var errBackendNotInitialised = errors.New("nodeshell: backend not initialised")

// resolveDataDir is a seam so tests can pin the data directory without
// touching the real user profile.
var resolveDataDir = configdir.DataDir

// listFonts is the seam for FontsList; production delegates to the stateless
// internal/fonts package, tests inject a fake. Font enumeration needs no
// service, so FontsList stays available before startup (Electron parity).
var listFonts = fonts.List

// newMcpRegistration is the seam for startup's MCP registration service.
// Production uses the stateless mcpregistration package (os.Executable +
// os.UserHomeDir); tests inject a service pinned to a fake executable and
// home so binding tests never touch the real user's MCP configs.
var newMcpRegistration = mcpregistration.New

// newAgentKeyBackend is the seam for the assistant's API-key store. It is the
// same OS keyring the host credentials use; tests inject an in-memory backend
// so no binding test writes to the real keyring. Each provider's key is stored
// under agent-api-key:<providerId>. The unprefixed agent-api-key account is
// the pre-multi-provider location and is copied once on migrate.
var newAgentKeyBackend = func() credentials.Backend { return keyring.NewBackend() }

const (
	agentKeyAccount       = "agent-api-key"
	agentKeyAccountPrefix = "agent-api-key:"
	agentKeyMaxLen        = 4096
)

func agentKeyAccountFor(providerID string) string {
	return agentKeyAccountPrefix + providerID
}

// App is the narrow Wails-bound facade. Hosts and settings methods delegate
// to the domain stores; known-hosts stays an internal service until the SSH
// task consumes it. Credentials are stored in the OS keyring; sessions,
// SFTP, monitor, fonts and MCP registration ride the services wired by
// startup.
//
// mu guards the service pointers: startup wires them and bound methods read
// them, and the WebView can call back before OnStartup has finished. Reads
// happen under the shared lock; a nil pointer is reported as an observable
// error (never fake success), so a call racing with startup either fails
// loudly or runs against a fully wired store.
type App struct {
	mu       sync.RWMutex
	dataDir  string
	hosts    *hosts.Store
	settings *settings.Store
	known    *knownhosts.Store
	creds    *credentials.Store
	readKey  credentials.PrivateKeyReader
	sessions *sessions.Manager
	sftp     *sftpservice.Service
	transfer *sftpservice.TransferManager
	monitor  *monitor.Service
	tunnels  *tunnel.Service
	// agent is the sidebar assistant; it runs tools only against sessions
	// this manager owns, so it can never reach a host the user is not on.
	agent *agent.Service
	// agentKeys stores the assistant's API key in the OS keyring, separately
	// from the per-host credentials store.
	agentKeys credentials.Backend
	// perms gates sensitive agent (and, in --mcp, MCP) tool calls. The GUI
	// uses permGate to wait on the in-app permission modal.
	perms    *permission.Service
	permGate *permission.ChannelGate
	// mcpReg writes the native MCP launcher config into Cursor / Claude Code
	// / Codex / OpenCode (this executable with --mcp).
	mcpReg *mcpregistration.Service
	// home is the symlink-resolved user home boundary for local paths
	// (upload sources, download targets); an empty home rejects every local
	// path in the SFTP service.
	home string
	// ctx is the Wails runtime context captured at OnStartup. It is nil
	// outside the GUI (unit tests), where session events have no receiver.
	ctx context.Context
}

// NewApp constructs an App whose services are initialised from the OS data
// directory during startup.
func NewApp() *App {
	return &App{}
}

// NewAppWithServices constructs an App with pre-wired services (test
// injection); startup leaves an already-wired App untouched. The sessions
// manager is wired when both the host store and the known-hosts store exist,
// and the SFTP and monitor services ride the same session manager and sink.
// home is the local-path boundary used by uploads and downloads.
func NewAppWithServices(dataDir string, h *hosts.Store, s *settings.Store, k *knownhosts.Store, c *credentials.Store, readKey credentials.PrivateKeyReader, home string) *App {
	app := &App{dataDir: dataDir, hosts: h, settings: s, known: k, creds: c, readKey: readKey, home: home,
		mcpReg: newMcpRegistration(), agentKeys: newAgentKeyBackend()}
	sink := &disposeSink{
		next:    &wailsSink{},
		sftp:    func() *sftpservice.Service { return app.sftp },
		monitor: func() *monitor.Service { return app.monitor },
		tunnels: func() *tunnel.Service { return app.tunnels },
		agent:   func() *agent.Service { return app.agent },
		perms:   func() *permission.Service { return app.perms },
	}
	app.wireSessions(h, k, c, readKey, sink)
	app.sftp = sftpservice.New(sftpservice.Deps{Opener: app.sessions, Sink: sink, Home: home})
	app.transfer = sftpservice.NewTransferManager(sftpservice.TransferManagerDeps{SFTP: app.sftp, Sink: sink, Home: home})
	app.wireMonitor(sink)
	app.wireTunnels()
	app.wirePermission(sink)
	app.wireAgent(sink)
	return app
}

// wireSessions builds the sessions manager from the stores, guarded against a
// partially wired App. The sink drops events when its context is nil. Events
// flow through disposeSink so a closed session also releases its cached SFTP
// client and stops its monitor poller.
func (a *App) wireSessions(h *hosts.Store, k *knownhosts.Store, c *credentials.Store, readKey credentials.PrivateKeyReader, sink sessions.EventSink) {
	if h == nil || k == nil {
		return
	}
	a.sessions = sessions.New(sessions.Deps{
		Hosts:    h,
		HostKeys: k,
		Creds:    c,
		ReadKey:  readKey,
		Sink:     sink,
	})
}

// wireMonitor builds the monitor service over the sessions execer; without a
// sessions manager there is nothing to poll, so the monitor stays nil and
// MonitorSetActive fails observably.
func (a *App) wireMonitor(sink sessions.EventSink) {
	if a.sessions == nil {
		return
	}
	a.monitor = monitor.New(monitor.Deps{Execer: a.sessions, Sink: sink})
}

// wireTunnels builds the local-forward service over the same session manager
// used for exec and SFTP. Without a sessions manager there is nothing to
// forward through, so the service stays nil and the bindings fail observably.
func (a *App) wireTunnels() {
	if a.sessions == nil {
		return
	}
	a.tunnels = tunnel.New(tunnel.Deps{
		Execer: a.sessions,
		Dialer: a.sessions,
		Ready:  a.sessions,
	})
}

// wireAgent builds the sidebar assistant over a guest MCP runtime on the GUI
// session manager and SFTP service. The BoundCaller injects the current tab's
// session so the model cannot pick another host or a local path. Without a
// session manager there is nothing to operate on, so the agent stays nil and
// its bindings fail observably. Endpoint config is resolved per prompt from
// the named provider list, which is how a key or model changed in settings
// applies without rebuilding the service. The guest runtime never starts an
// idle reaper.
func (a *App) wireAgent(sink sessions.EventSink) {
	if a.sessions == nil || a.sftp == nil {
		return
	}
	rt := mcpcli.New(mcpcli.Deps{
		Manager:       a.sessions,
		SFTP:          a.sftp,
		Auth:          a.perms,
		GuestSessions: true,
	})
	a.agent = agent.New(agent.Deps{
		Tools: &agent.BoundCaller{MCP: rt},
		Sink:  sink,
	})
	// Do not StartReaper: this runtime shares GUI sessions, and idle reap
	// would disconnect the tab the user is looking at.
}

// wirePermission builds the in-app permission gate over the same Wails sink
// the agent uses, so a sensitive tool blocks on the renderer modal instead
// of running immediately. MCP stdio mode never calls this: it has no
// WebView and uses NativeGate instead.
func (a *App) wirePermission(sink sessions.EventSink) {
	gate := permission.NewChannelGate(sink)
	a.permGate = gate
	a.perms = permission.NewService(permission.ServiceDeps{
		Gate:   gate,
		Policy: a.permissionPolicy,
	})
}

func (a *App) permissionPolicy() permission.Policy {
	a.mu.RLock()
	s := a.settings
	a.mu.RUnlock()
	if s == nil {
		return permission.PolicyAsk
	}
	current, err := s.Get()
	if err != nil {
		return permission.PolicyAsk
	}
	return permission.ParsePolicy(current.PermissionPolicy)
}

// disposeSink forwards every event to the next sink and, on session:closed,
// disposes the session's cached SFTP client, stops its monitor poller,
// closes its local port forwards and drops its agent conversation — a
// torn-down SSH session can never leave a stale SFTP channel, a polling
// goroutine, a listening local port or a running agent loop behind. The
// getters resolve lazily so wiring order (sessions before sftp/monitor/agent)
// never matters.
type disposeSink struct {
	next    sessions.EventSink
	sftp    func() *sftpservice.Service
	monitor func() *monitor.Service
	tunnels func() *tunnel.Service
	agent   func() *agent.Service
	perms   func() *permission.Service
}

func (s *disposeSink) Emit(event string, payload any) {
	if event == sessions.EventSessionClosed {
		if e, ok := payload.(sessions.ClosedEvent); ok {
			s.disposeSession(e.SessionID)
		}
	}
	if s.next != nil {
		s.next.Emit(event, payload)
	}
}

// disposeSession releases everything the closed session owned. Each getter is
// optional, so a sink wired for one service only (unit tests) cannot panic
// here.
func (s *disposeSink) disposeSession(sessionID string) {
	if s.sftp != nil {
		if svc := s.sftp(); svc != nil {
			svc.Dispose(sessionID)
		}
	}
	if s.monitor != nil {
		if m := s.monitor(); m != nil {
			m.Dispose(sessionID)
		}
	}
	if s.tunnels != nil {
		if tun := s.tunnels(); tun != nil {
			tun.Dispose(sessionID)
		}
	}
	// A closed session's conversation is dropped with it, so a reconnect
	// never inherits the previous connection's transcript and no agent loop
	// outlives its SSH session.
	if s.agent != nil {
		if ag := s.agent(); ag != nil {
			ag.Dispose(sessionID)
		}
	}
	if s.perms != nil {
		if p := s.perms(); p != nil {
			p.ForgetSession(sessionID)
		}
	}
}

// boolPtr returns a pointer to b, for hosts.Patch flag fields.
func boolPtr(b bool) *bool { return &b }

// logRollbackFailure records a failed credential rollback to stderr. The
// message is generic — never a host id, path or secret — and the original
// error the caller is about to return is never replaced.
func logRollbackFailure(err error) {
	fmt.Fprintf(os.Stderr, "nodeshell: failed to restore previous credentials: %v\n", err)
}

// startup is invoked by Wails once the WebView is initialised. It resolves
// and creates the data directory and wires the domain services. Failures are
// written to stderr; bound methods keep failing observably until services
// exist rather than returning partial data.
func (a *App) startup(ctx context.Context) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.mcpReg == nil {
		a.mcpReg = newMcpRegistration()
	}
	if a.agentKeys == nil {
		a.agentKeys = newAgentKeyBackend()
	}
	if a.hosts != nil && a.settings != nil {
		return
	}
	dir, err := resolveDataDir()
	if err != nil {
		fmt.Fprintf(os.Stderr, "nodeshell: resolve data dir: %v\n", err)
		return
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		fmt.Fprintf(os.Stderr, "nodeshell: create data dir: %v\n", err)
		return
	}
	a.dataDir = dir
	a.hosts = hosts.New(dir)
	a.settings = settings.New(dir)
	a.known = knownhosts.New(dir)
	if err := a.known.Load(); err != nil {
		fmt.Fprintf(os.Stderr, "nodeshell: load known hosts: %v\n", err)
	}
	// Credentials live in the OS keyring, not in the data dir, so no path
	// under dir is ever passed to the credentials service — the old Electron
	// credentials.json stays untouched for rollback.
	a.creds = credentials.New(keyring.NewBackend())
	home, err := os.UserHomeDir()
	if err != nil {
		fmt.Fprintf(os.Stderr, "nodeshell: resolve user home: %v\n", err)
	}
	a.home = home
	a.readKey = credentials.NewHomeReader(home)
	// The runtime context is captured for event emission; a nil context (the
	// ctx can only come from Wails OnStartup) simply drops events.
	a.ctx = ctx
	sink := &disposeSink{
		next:    &wailsSink{ctx: ctx},
		sftp:    func() *sftpservice.Service { return a.sftp },
		monitor: func() *monitor.Service { return a.monitor },
		tunnels: func() *tunnel.Service { return a.tunnels },
		agent:   func() *agent.Service { return a.agent },
		perms:   func() *permission.Service { return a.perms },
	}
	a.wireSessions(a.hosts, a.known, a.creds, a.readKey, sink)
	a.sftp = sftpservice.New(sftpservice.Deps{Opener: a.sessions, Sink: sink, Home: home})
	a.transfer = sftpservice.NewTransferManager(sftpservice.TransferManagerDeps{SFTP: a.sftp, Sink: sink, Home: home})
	a.wireMonitor(sink)
	a.wireTunnels()
	a.wirePermission(sink)
	a.wireAgent(sink)
}

// shutdown is the Wails OnShutdown hook: the agent loops and the monitor
// poller are stopped before the sessions, so nothing is ever mid-exec against
// a torn-down session and no event can be emitted while the WebView tears
// down. Sessions and SFTP clients are then disposed quietly.
func (a *App) shutdown(context.Context) {
	a.mu.RLock()
	m := a.sessions
	svc := a.sftp
	tm := a.transfer
	mon := a.monitor
	tun := a.tunnels
	ag := a.agent
	a.mu.RUnlock()
	if ag != nil {
		ag.DisposeAll()
	}
	if tm != nil {
		tm.Dispose()
	}
	if mon != nil {
		mon.DisposeAll()
	}
	if tun != nil {
		tun.DisposeAll()
	}
	if m != nil {
		m.DisposeAll()
	}
	if svc != nil {
		svc.DisposeAll()
	}
}

// HostsList returns all host configurations. Hosts whose persisted
// credentialsSaved flag is true but that have no keyring entry (old Electron
// saves never migrated into the OS keyring) or an unrecoverable (corrupt)
// entry are returned with the flag normalised to false, so the UI prompts for
// credentials instead of trusting a stale flag. The normalisation is
// view-only: the file is left as-is until a successful save persists the
// flags, and hosts without a stale flag never touch the keyring. Only a real
// backend failure is an observable error — a host whose secret state is
// unknown never takes down the rest of the list.
func (a *App) HostsList() ([]hosts.HostConfig, error) {
	a.mu.RLock()
	h := a.hosts
	creds := a.creds
	a.mu.RUnlock()
	if h == nil {
		return nil, errBackendNotInitialised
	}
	list, err := h.List()
	if err != nil {
		return nil, err
	}
	if creds == nil {
		return list, nil
	}
	for i := range list {
		if !list[i].CredentialsSaved {
			continue
		}
		_, found, err := creds.Get(list[i].Id)
		if err != nil {
			// A corrupt entry means this host's secret state is unknowable:
			// normalise the stale flags away and keep the other hosts.
			// Anything else (backend down) must reach the caller.
			if !errors.Is(err, credentials.ErrCorrupt) {
				return nil, err
			}
			list[i].CredentialsSaved = false
			list[i].CredentialsPrompted = false
			continue
		}
		if !found {
			list[i].CredentialsSaved = false
			list[i].CredentialsPrompted = false
		}
	}
	return list, nil
}

// HostsCreate adds a host and returns it with its generated id.
func (a *App) HostsCreate(input hosts.HostInput) (hosts.HostConfig, error) {
	a.mu.RLock()
	h := a.hosts
	a.mu.RUnlock()
	if h == nil {
		return hosts.HostConfig{}, errBackendNotInitialised
	}
	return h.Create(input)
}

// HostsUpdate applies patch to the host with the given id.
func (a *App) HostsUpdate(id string, patch hosts.Patch) (hosts.HostConfig, error) {
	a.mu.RLock()
	h := a.hosts
	a.mu.RUnlock()
	if h == nil {
		return hosts.HostConfig{}, errBackendNotInitialised
	}
	return h.Update(id, patch)
}

// HostsRemove deletes the host with the given id and its keyring secrets.
// The keyring entry is deleted first (a missing entry counts as success) so a
// host can never be removed while its secret lingers; any real keyring delete
// error aborts the removal, leaving the host in place. If the host-store
// write fails after the keyring was cleared, the previous secret is restored
// so it never disappears while the host survives.
func (a *App) HostsRemove(id string) error {
	a.mu.RLock()
	h := a.hosts
	creds := a.creds
	a.mu.RUnlock()
	if h == nil {
		return errBackendNotInitialised
	}
	if creds == nil {
		return h.Remove(id)
	}
	prev, prevFound, err := creds.Get(id)
	if err != nil {
		// A corrupt entry can still be cleared; there is nothing meaningful
		// to roll back to if the host-store write fails.
		prevFound = false
	}
	if err := creds.Clear(id); err != nil {
		return err
	}
	if err := h.Remove(id); err != nil {
		if prevFound {
			if err := creds.Save(id, credentials.SavePatch{Password: &prev.Password, PrivateKey: &prev.PrivateKey}); err != nil {
				logRollbackFailure(err)
			}
		}
		return err
	}
	return nil
}

// SettingsGet returns the current settings.
func (a *App) SettingsGet() (settings.AppSettings, error) {
	a.mu.RLock()
	s := a.settings
	a.mu.RUnlock()
	if s == nil {
		return settings.AppSettings{}, errBackendNotInitialised
	}
	return s.Get()
}

// SettingsSet merges patch into the settings, persists and returns the result.
func (a *App) SettingsSet(patch settings.Patch) (settings.AppSettings, error) {
	a.mu.RLock()
	s := a.settings
	a.mu.RUnlock()
	if s == nil {
		return settings.AppSettings{}, errBackendNotInitialised
	}
	return s.Set(patch)
}

// CredentialsIsAvailable reports whether the OS keyring is worth attempting.
func (a *App) CredentialsIsAvailable() (bool, error) {
	a.mu.RLock()
	c := a.creds
	a.mu.RUnlock()
	if c == nil {
		return false, errBackendNotInitialised
	}
	return c.Available(), nil
}

// CredentialsSave stores secrets for the host in the OS keyring and marks the
// host saved. The Electron save payload {password?, privateKeyPath?} is
// accepted; privateKeyPath is resolved inside the user home directory and read
// (symlinks re-validated) before anything is stored. The host must exist: an
// unknown or empty host id is rejected before any keyring write, so an orphan
// secret can never be created. A successful save persists
// credentialsPrompted=true and credentialsSaved=true exactly like the
// Electron main did; a failed save leaves the host flags untouched, and if
// the keyring write succeeded but the flag update failed, the keyring is
// rolled back to the previous credential.
func (a *App) CredentialsSave(hostId string, payload credentials.SavePayload) error {
	a.mu.RLock()
	c := a.creds
	h := a.hosts
	readKey := a.readKey
	a.mu.RUnlock()
	if c == nil || h == nil {
		return errBackendNotInitialised
	}
	// The keyring must never receive an account for an unknown or empty host:
	// validate existence first (a read failure keeps its CONFIG_READ_FAILED
	// code) so orphan secrets cannot be created for ids that do not exist.
	if _, ok, err := h.GetByID(hostId); err != nil {
		return err
	} else if !ok {
		return &hosts.Error{Code: apperror.Unknown, Message: fmt.Sprintf("Host not found: %s", hostId)}
	}
	// Remember the previous credential so a failed flag update can roll the
	// keyring back instead of leaving a saved secret with an unsaved flag.
	prev, prevFound, err := c.Get(hostId)
	if err != nil {
		return err
	}
	patch := credentials.SavePatch{}
	if payload.Password != nil && *payload.Password != "" {
		patch.Password = payload.Password
	}
	if payload.PrivateKeyPath != nil && *payload.PrivateKeyPath != "" {
		if readKey == nil {
			return errBackendNotInitialised
		}
		content, err := readKey(*payload.PrivateKeyPath)
		if err != nil {
			return err
		}
		patch.PrivateKey = &content
	}
	if err := c.Save(hostId, patch); err != nil {
		return err
	}
	if _, err := h.Update(hostId, hosts.Patch{CredentialsPrompted: boolPtr(true), CredentialsSaved: boolPtr(true)}); err != nil {
		// The keyring write succeeded but the flag update failed: roll the
		// keyring back so no secret survives without its saved flag.
		if prevFound {
			if err := c.Save(hostId, credentials.SavePatch{Password: &prev.Password, PrivateKey: &prev.PrivateKey}); err != nil {
				logRollbackFailure(err)
			}
		} else if err := c.Clear(hostId); err != nil {
			logRollbackFailure(err)
		}
		return err
	}
	return nil
}

// CredentialsClear removes the host's keyring entry (missing counts as
// success) and persists credentialsSaved=false, mirroring the Electron main.
// If the flag update fails after the keyring was cleared, the previous secret
// is restored.
func (a *App) CredentialsClear(hostId string) error {
	a.mu.RLock()
	c := a.creds
	h := a.hosts
	a.mu.RUnlock()
	if c == nil || h == nil {
		return errBackendNotInitialised
	}
	prev, prevFound, err := c.Get(hostId)
	if err != nil {
		// A corrupt entry can still be cleared; there is nothing meaningful
		// to roll back to if the flag update fails.
		prevFound = false
	}
	if err := c.Clear(hostId); err != nil {
		return err
	}
	if _, err := h.Update(hostId, hosts.Patch{CredentialsSaved: boolPtr(false)}); err != nil {
		if prevFound {
			if err := c.Save(hostId, credentials.SavePatch{Password: &prev.Password, PrivateKey: &prev.PrivateKey}); err != nil {
				logRollbackFailure(err)
			}
		}
		return err
	}
	return nil
}

// CredentialsMarkPrompted records that the user was asked about saving
// credentials, mirroring the Electron main. saved=true is only honoured when
// a keyring entry for the host really exists; otherwise it is forced to false
// so the host can never be marked saved without a stored secret.
func (a *App) CredentialsMarkPrompted(hostId string, saved bool) error {
	a.mu.RLock()
	c := a.creds
	h := a.hosts
	a.mu.RUnlock()
	if c == nil {
		return errBackendNotInitialised
	}
	if saved {
		_, found, err := c.Get(hostId)
		if err != nil {
			return err
		}
		saved = found
	}
	if h == nil {
		return errBackendNotInitialised
	}
	_, err := h.Update(hostId, hosts.Patch{CredentialsPrompted: boolPtr(true), CredentialsSaved: boolPtr(saved)})
	return err
}

// SessionsConnect establishes an interactive SSH session for the host. The
// connect runs in the background of the Wails runtime; the returned promise
// resolves once the connection, PTY and shell are fully usable, or fails with
// a stable SSH error code.
func (a *App) SessionsConnect(hostID string, opts sessions.ConnectOptions) (sessions.ConnectResult, error) {
	a.mu.RLock()
	m := a.sessions
	a.mu.RUnlock()
	if m == nil {
		return sessions.ConnectResult{}, errBackendNotInitialised
	}
	return m.Connect(context.Background(), hostID, opts)
}

// SessionsWrite sends terminal input to the session's stdin. Input is
// fire-and-forget from the frontend; failures are returned for the adapter to
// surface observably.
func (a *App) SessionsWrite(sessionID string, data string) error {
	a.mu.RLock()
	m := a.sessions
	a.mu.RUnlock()
	if m == nil {
		return errBackendNotInitialised
	}
	return m.Write(sessionID, data)
}

// SessionsResize forwards an SSH window-change request for the session.
func (a *App) SessionsResize(sessionID string, cols, rows int) error {
	a.mu.RLock()
	m := a.sessions
	a.mu.RUnlock()
	if m == nil {
		return errBackendNotInitialised
	}
	return m.Resize(sessionID, cols, rows)
}

// SessionsDisconnect ends the session; unknown ids are a no-op success
// (Electron parity).
func (a *App) SessionsDisconnect(sessionID string) error {
	a.mu.RLock()
	m := a.sessions
	a.mu.RUnlock()
	if m == nil {
		return errBackendNotInitialised
	}
	return m.Disconnect(sessionID)
}

// SessionsCancelConnect aborts every in-flight connect; established sessions
// are unaffected.
func (a *App) SessionsCancelConnect() error {
	a.mu.RLock()
	m := a.sessions
	a.mu.RUnlock()
	if m == nil {
		return errBackendNotInitialised
	}
	m.CancelConnect()
	return nil
}

// MonitorSetActive starts polling the session for the remote Linux monitor,
// or clears the monitor when sessionID is empty (the UI switches or closes a
// tab). Errors surface as monitor:update events and never touch the session;
// an uninitialised backend is an observable error, never fake success.
func (a *App) MonitorSetActive(sessionID, title string) error {
	a.mu.RLock()
	m := a.monitor
	a.mu.RUnlock()
	if m == nil {
		return errBackendNotInitialised
	}
	m.SetActive(sessionID, title)
	return nil
}

func (a *App) tunnelService() (*tunnel.Service, error) {
	a.mu.RLock()
	svc := a.tunnels
	a.mu.RUnlock()
	if svc == nil {
		return nil, errBackendNotInitialised
	}
	return svc, nil
}

// TunnelsDiscover lists TCP ports currently listening on the remote session.
func (a *App) TunnelsDiscover(sessionID string) ([]tunnel.Listener, error) {
	svc, err := a.tunnelService()
	if err != nil {
		return nil, err
	}
	return svc.Discover(context.Background(), sessionID)
}

// TunnelsStart opens a local 127.0.0.1 listener that forwards to the remote
// address and port over the SSH session.
func (a *App) TunnelsStart(sessionID, remoteAddr string, remotePort int) (tunnel.Tunnel, error) {
	svc, err := a.tunnelService()
	if err != nil {
		return tunnel.Tunnel{}, err
	}
	return svc.Start(sessionID, remoteAddr, remotePort)
}

// TunnelsStop closes one local forward. Unknown ids are a no-op success.
func (a *App) TunnelsStop(sessionID, tunnelID string) error {
	svc, err := a.tunnelService()
	if err != nil {
		return err
	}
	return svc.Stop(sessionID, tunnelID)
}

// TunnelsList returns the live local forwards for the session.
func (a *App) TunnelsList(sessionID string) ([]tunnel.Tunnel, error) {
	svc, err := a.tunnelService()
	if err != nil {
		return nil, err
	}
	return svc.List(sessionID), nil
}

// --- SFTP bindings (ElectronApi.sftp contract) ---

func (a *App) sftpService() (*sftpservice.Service, error) {
	a.mu.RLock()
	svc := a.sftp
	a.mu.RUnlock()
	if svc == nil {
		return nil, errBackendNotInitialised
	}
	return svc, nil
}

// SftpList returns the session's current remote directory listing.
func (a *App) SftpList(sessionID string) ([]sftpservice.Entry, error) {
	svc, err := a.sftpService()
	if err != nil {
		return nil, err
	}
	return svc.List(sessionID, "")
}

// SftpCwd returns the session's current remote directory.
func (a *App) SftpCwd(sessionID string) (string, error) {
	svc, err := a.sftpService()
	if err != nil {
		return "", err
	}
	return svc.Cwd(sessionID)
}

// SftpChdir changes the session's remote directory and returns the new one.
func (a *App) SftpChdir(sessionID, remotePath string) (string, error) {
	svc, err := a.sftpService()
	if err != nil {
		return "", err
	}
	return svc.Chdir(sessionID, remotePath)
}

// SftpMkdir creates a directory under the session's current remote directory.
func (a *App) SftpMkdir(sessionID, name string) error {
	svc, err := a.sftpService()
	if err != nil {
		return err
	}
	return svc.Mkdir(sessionID, name)
}

// SftpRename moves a remote entry under the session's current directory.
func (a *App) SftpRename(sessionID, from, to string) error {
	svc, err := a.sftpService()
	if err != nil {
		return err
	}
	return svc.Rename(sessionID, from, to)
}

// SftpRemove deletes a remote entry recursively (never following symlinks).
func (a *App) SftpRemove(sessionID, remotePath string) error {
	svc, err := a.sftpService()
	if err != nil {
		return err
	}
	return svc.Remove(sessionID, remotePath)
}

// openUploadDialog is a seam: production opens the Wails multi-file dialog;
// tests inject a fake (the runtime dialogs fatal-exit on a non-Wails
// context). An empty result means the user cancelled.
var openUploadDialog = func(ctx context.Context) ([]string, error) {
	return runtime.OpenMultipleFilesDialog(ctx, runtime.OpenDialogOptions{Title: "Upload files"})
}

// openSaveDialog is the seam for the download save dialog; an empty result
// means the user cancelled.
var openSaveDialog = func(ctx context.Context, defaultName string) (string, error) {
	return runtime.SaveFileDialog(ctx, runtime.SaveDialogOptions{Title: "Save file", DefaultFilename: defaultName})
}

// openPrivateKeyDialog is the seam for the private-key picker; an empty
// result means the user cancelled.
var openPrivateKeyDialog = func(ctx context.Context) (string, error) {
	return runtime.OpenFileDialog(ctx, runtime.OpenDialogOptions{Title: "Select private key"})
}

// dialogCtx returns the runtime context or a coded error when the app is not
// running inside the GUI (unit tests), where dialogs would fatal-exit.
func (a *App) dialogCtx() (context.Context, error) {
	a.mu.RLock()
	ctx := a.ctx
	a.mu.RUnlock()
	if ctx == nil {
		return nil, &sftpservice.Error{Code: apperror.Unknown, Message: "File dialogs are unavailable outside the GUI"}
	}
	return ctx, nil
}

// SftpUpload opens the multi-file selection dialog and uploads every chosen
// file into the session's current remote directory.
func (a *App) SftpUpload(sessionID string) error {
	svc, err := a.sftpService()
	if err != nil {
		return err
	}
	ctx, err := a.dialogCtx()
	if err != nil {
		return err
	}
	paths, err := openUploadDialog(ctx)
	if err != nil {
		return err
	}
	if len(paths) == 0 {
		return nil // cancelled
	}
	return svc.UploadPaths(sessionID, paths)
}

// SftpUploadPaths validates and uploads the given local paths (drag-drop or
// dialog results). Paths must resolve inside the user home directory; non-
// files are skipped.
func (a *App) SftpUploadPaths(sessionID string, localPaths []string) error {
	svc, err := a.sftpService()
	if err != nil {
		return err
	}
	return svc.UploadPaths(sessionID, localPaths)
}

// SftpDownload opens the save dialog with defaultName and downloads the
// remote file into the chosen (home-boundary checked) target.
func (a *App) SftpDownload(sessionID, remotePath, defaultName string) error {
	svc, err := a.sftpService()
	if err != nil {
		return err
	}
	ctx, err := a.dialogCtx()
	if err != nil {
		return err
	}
	target, err := openSaveDialog(ctx, defaultName)
	if err != nil {
		return err
	}
	if target == "" {
		return nil // cancelled
	}
	return svc.Download(sessionID, remotePath, target)
}

// sftpGUITextMaxBytes is the GUI text-editor cap; kept identical to the MCP
// MaxFileBytes (512KiB) so the same remote files are editable in both paths.
const sftpGUITextMaxBytes int64 = 512 * 1024

// SftpTextContent is the SftpReadText IPC payload.
type SftpTextContent struct {
	Path    string `json:"path"`
	Content string `json:"content"`
}

// SftpTextPath is the SftpWriteText IPC payload.
type SftpTextPath struct {
	Path string `json:"path"`
}

// SftpReadText reads a remote text file (512KiB cap) for the in-app editor.
func (a *App) SftpReadText(sessionID, remotePath string) (SftpTextContent, error) {
	svc, err := a.sftpService()
	if err != nil {
		return SftpTextContent{}, err
	}
	resolved, content, err := svc.ReadText(sessionID, remotePath, sftpGUITextMaxBytes)
	if err != nil {
		return SftpTextContent{}, err
	}
	return SftpTextContent{Path: resolved, Content: content}, nil
}

// SftpWriteText writes UTF-8 text to a remote file (512KiB cap) from the
// in-app editor. The service commits via temp+rename so a failed write never
// truncates an existing target.
func (a *App) SftpWriteText(sessionID, remotePath, content string) (SftpTextPath, error) {
	svc, err := a.sftpService()
	if err != nil {
		return SftpTextPath{}, err
	}
	resolved, err := svc.WriteText(sessionID, remotePath, content, sftpGUITextMaxBytes)
	if err != nil {
		return SftpTextPath{}, err
	}
	return SftpTextPath{Path: resolved}, nil
}

// --- Agent bindings (ElectronApi.agent contract) ---

// AgentProviderStatus is one named provider as returned to the renderer. The
// API key is never included, only whether one is stored.
type AgentProviderStatus struct {
	ID      string   `json:"id"`
	Name    string   `json:"name"`
	BaseURL string   `json:"baseUrl"`
	Models  []string `json:"models"`
	HasKey  bool     `json:"hasKey"`
}

// AgentConfigStatus is the AgentStatus payload. The API key is never
// returned, only whether each provider has one stored.
type AgentConfigStatus struct {
	Configured        bool                  `json:"configured"`
	Providers         []AgentProviderStatus `json:"providers"`
	DefaultProviderID string                `json:"defaultProviderId"`
	DefaultModel      string                `json:"defaultModel"`
}

// AgentProviderInput is the upsert payload. An empty ID creates a provider.
type AgentProviderInput struct {
	ID      string   `json:"id"`
	Name    string   `json:"name"`
	BaseURL string   `json:"baseUrl"`
	Models  []string `json:"models"`
}

// agentKeyBackend returns the keyring backend for the assistant's API keys.
func (a *App) agentKeyBackend() (credentials.Backend, error) {
	a.mu.RLock()
	keys := a.agentKeys
	a.mu.RUnlock()
	if keys == nil {
		return nil, errBackendNotInitialised
	}
	return keys, nil
}

// agentAPIKeyFor reads one provider's stored key; a missing entry is an empty
// key, not an error, so an unconfigured assistant reports "not configured"
// instead of a keyring failure. The value stays inside this process.
func (a *App) agentAPIKeyFor(providerID string) (string, error) {
	keys, err := a.agentKeyBackend()
	if err != nil {
		return "", err
	}
	value, err := keys.Get(credentials.ServiceName, agentKeyAccountFor(providerID))
	if err != nil {
		if errors.Is(err, credentials.ErrNotFound) {
			return "", nil
		}
		return "", &agentKeyError{message: "Failed to read the agent API key"}
	}
	return value, nil
}

// agentKeyError is the coded, secret-free error surfaced for keyring failures
// on the agent path.
type agentKeyError struct{ message string }

func (e *agentKeyError) Error() string     { return e.message }
func (e *agentKeyError) ErrorCode() string { return apperror.Unknown }

func (a *App) settingsStore() (*settings.Store, error) {
	a.mu.RLock()
	s := a.settings
	a.mu.RUnlock()
	if s == nil {
		return nil, errBackendNotInitialised
	}
	return s, nil
}

// migrateAgentProviders copies a pre-multi-provider key onto the legacy
// provider account and persists a synthesised provider list when the file
// still uses the old single-endpoint fields.
func (a *App) migrateAgentProviders() error {
	s, err := a.settingsStore()
	if err != nil {
		return err
	}
	keys, err := a.agentKeyBackend()
	if err != nil {
		return err
	}
	legacyKey, err := keys.Get(credentials.ServiceName, agentKeyAccount)
	if err != nil && !errors.Is(err, credentials.ErrNotFound) {
		return &agentKeyError{message: "Failed to read the agent API key"}
	}
	if err == nil && legacyKey != "" {
		dest := agentKeyAccountFor(settings.LegacyProviderID)
		_, destErr := keys.Get(credentials.ServiceName, dest)
		if destErr != nil && !errors.Is(destErr, credentials.ErrNotFound) {
			return &agentKeyError{message: "Failed to read the agent API key"}
		}
		if errors.Is(destErr, credentials.ErrNotFound) {
			if err := keys.Set(credentials.ServiceName, dest, legacyKey); err != nil {
				return &agentKeyError{message: "Failed to store the agent API key"}
			}
		}
		if err := keys.Delete(credentials.ServiceName, agentKeyAccount); err != nil &&
			!errors.Is(err, credentials.ErrNotFound) {
			return &agentKeyError{message: "Failed to clear the agent API key"}
		}
	}

	current, err := s.Get()
	if err != nil {
		return err
	}
	if current.AgentProvidersPresent {
		return nil
	}
	providers := current.AgentProviders
	if len(providers) == 0 {
		if legacyKey == "" {
			return nil
		}
		providers = []settings.AgentProvider{{
			ID:      settings.LegacyProviderID,
			Name:    settings.LegacyProviderName,
			BaseURL: current.AgentBaseURL,
			Models:  []string{current.AgentModel},
		}}
	}
	defaultID, defaultModel := current.AgentDefaultProviderID, current.AgentDefaultModel
	if _, err := s.Set(settings.Patch{
		AgentProviders:         &providers,
		AgentDefaultProviderID: &defaultID,
		AgentDefaultModel:      &defaultModel,
	}); err != nil {
		return err
	}
	return nil
}

func providerHasModel(p settings.AgentProvider, model string) bool {
	for _, m := range p.Models {
		if m == model {
			return true
		}
	}
	return false
}

func findProvider(providers []settings.AgentProvider, id string) (settings.AgentProvider, bool) {
	for _, p := range providers {
		if p.ID == id {
			return p, true
		}
	}
	return settings.AgentProvider{}, false
}

// agentConfigFor resolves one named provider + model into the endpoint config
// the agent loop needs. Unknown ids and missing keys fail before a run starts.
func (a *App) agentConfigFor(providerID, model string) (agent.Config, error) {
	if err := a.migrateAgentProviders(); err != nil {
		return agent.Config{}, err
	}
	s, err := a.settingsStore()
	if err != nil {
		return agent.Config{}, err
	}
	current, err := s.Get()
	if err != nil {
		return agent.Config{}, err
	}
	p, ok := findProvider(current.AgentProviders, strings.TrimSpace(providerID))
	if !ok || !providerHasModel(p, strings.TrimSpace(model)) {
		return agent.Config{}, &agentKeyError{message: "Unknown agent model"}
	}
	key, err := a.agentAPIKeyFor(p.ID)
	if err != nil {
		return agent.Config{}, err
	}
	if key == "" {
		return agent.Config{}, agent.ErrNotConfigured
	}
	return agent.Config{BaseURL: p.BaseURL, Model: strings.TrimSpace(model), APIKey: key}, nil
}

func (a *App) agentStatusFrom(current settings.AppSettings) (AgentConfigStatus, error) {
	views := make([]AgentProviderStatus, 0, len(current.AgentProviders))
	configured := false
	for _, p := range current.AgentProviders {
		key, err := a.agentAPIKeyFor(p.ID)
		if err != nil {
			return AgentConfigStatus{}, err
		}
		hasKey := key != ""
		if hasKey && len(p.Models) > 0 {
			configured = true
		}
		models := p.Models
		if models == nil {
			models = []string{}
		}
		views = append(views, AgentProviderStatus{
			ID:      p.ID,
			Name:    p.Name,
			BaseURL: p.BaseURL,
			Models:  models,
			HasKey:  hasKey,
		})
	}
	defaultID, defaultModel := current.AgentDefaultProviderID, current.AgentDefaultModel
	if p, ok := findProvider(current.AgentProviders, defaultID); !ok || !providerHasModel(p, defaultModel) {
		defaultID, defaultModel = "", ""
		for _, v := range views {
			if v.HasKey && len(v.Models) > 0 {
				defaultID, defaultModel = v.ID, v.Models[0]
				break
			}
		}
		if defaultID == "" && len(current.AgentProviders) > 0 && len(current.AgentProviders[0].Models) > 0 {
			defaultID, defaultModel = current.AgentProviders[0].ID, current.AgentProviders[0].Models[0]
		}
	}
	if views == nil {
		views = []AgentProviderStatus{}
	}
	return AgentConfigStatus{
		Configured:        configured,
		Providers:         views,
		DefaultProviderID: defaultID,
		DefaultModel:      defaultModel,
	}, nil
}

// AgentStatus reports the named providers and whether each has an API key
// stored (adapter agent.status).
func (a *App) AgentStatus() (AgentConfigStatus, error) {
	if err := a.migrateAgentProviders(); err != nil {
		return AgentConfigStatus{}, err
	}
	s, err := a.settingsStore()
	if err != nil {
		return AgentConfigStatus{}, err
	}
	current, err := s.Get()
	if err != nil {
		return AgentConfigStatus{}, err
	}
	return a.agentStatusFrom(current)
}

// AgentUpsertProvider creates or updates one named provider (never the key).
func (a *App) AgentUpsertProvider(input AgentProviderInput) (AgentConfigStatus, error) {
	if err := a.migrateAgentProviders(); err != nil {
		return AgentConfigStatus{}, err
	}
	s, err := a.settingsStore()
	if err != nil {
		return AgentConfigStatus{}, err
	}
	current, err := s.Get()
	if err != nil {
		return AgentConfigStatus{}, err
	}
	id := strings.TrimSpace(input.ID)
	creating := id == ""
	if creating {
		if len(current.AgentProviders) >= settings.AgentProviderMax {
			return AgentConfigStatus{}, &agentKeyError{message: "Too many agent providers"}
		}
		id = uuid.NewString()
	}
	next, ok := settings.NormalizeProvider(settings.AgentProvider{
		ID:      id,
		Name:    input.Name,
		BaseURL: input.BaseURL,
		Models:  input.Models,
	})
	if !ok {
		return AgentConfigStatus{}, &agentKeyError{message: "Invalid agent provider"}
	}
	providers := append([]settings.AgentProvider(nil), current.AgentProviders...)
	replaced := false
	for i, p := range providers {
		if p.ID == next.ID {
			providers[i] = next
			replaced = true
			break
		}
	}
	if !replaced {
		if len(providers) >= settings.AgentProviderMax {
			return AgentConfigStatus{}, &agentKeyError{message: "Too many agent providers"}
		}
		providers = append(providers, next)
	}
	defaultID, defaultModel := current.AgentDefaultProviderID, current.AgentDefaultModel
	if creating && defaultID == "" && len(next.Models) > 0 {
		defaultID, defaultModel = next.ID, next.Models[0]
	}
	if _, err := s.Set(settings.Patch{
		AgentProviders:         &providers,
		AgentDefaultProviderID: &defaultID,
		AgentDefaultModel:      &defaultModel,
	}); err != nil {
		return AgentConfigStatus{}, err
	}
	current, err = s.Get()
	if err != nil {
		return AgentConfigStatus{}, err
	}
	return a.agentStatusFrom(current)
}

// AgentDeleteProvider removes a provider and its keyring entry.
func (a *App) AgentDeleteProvider(id string) (AgentConfigStatus, error) {
	if err := a.migrateAgentProviders(); err != nil {
		return AgentConfigStatus{}, err
	}
	s, err := a.settingsStore()
	if err != nil {
		return AgentConfigStatus{}, err
	}
	current, err := s.Get()
	if err != nil {
		return AgentConfigStatus{}, err
	}
	id = strings.TrimSpace(id)
	providers := make([]settings.AgentProvider, 0, len(current.AgentProviders))
	found := false
	for _, p := range current.AgentProviders {
		if p.ID == id {
			found = true
			continue
		}
		providers = append(providers, p)
	}
	if !found {
		return AgentConfigStatus{}, &agentKeyError{message: "Unknown agent provider"}
	}
	if _, err := s.Set(settings.Patch{AgentProviders: &providers}); err != nil {
		return AgentConfigStatus{}, err
	}
	keys, err := a.agentKeyBackend()
	if err != nil {
		return AgentConfigStatus{}, err
	}
	if err := keys.Delete(credentials.ServiceName, agentKeyAccountFor(id)); err != nil &&
		!errors.Is(err, credentials.ErrNotFound) {
		return AgentConfigStatus{}, &agentKeyError{message: "Failed to clear the agent API key"}
	}
	current, err = s.Get()
	if err != nil {
		return AgentConfigStatus{}, err
	}
	return a.agentStatusFrom(current)
}

// AgentSetProviderKey stores or clears one provider's API key. An empty key
// clears the stored entry.
func (a *App) AgentSetProviderKey(id, apiKey string) (AgentConfigStatus, error) {
	if err := a.migrateAgentProviders(); err != nil {
		return AgentConfigStatus{}, err
	}
	s, err := a.settingsStore()
	if err != nil {
		return AgentConfigStatus{}, err
	}
	current, err := s.Get()
	if err != nil {
		return AgentConfigStatus{}, err
	}
	id = strings.TrimSpace(id)
	if _, ok := findProvider(current.AgentProviders, id); !ok {
		return AgentConfigStatus{}, &agentKeyError{message: "Unknown agent provider"}
	}
	keys, err := a.agentKeyBackend()
	if err != nil {
		return AgentConfigStatus{}, err
	}
	key := strings.TrimSpace(apiKey)
	if len(key) > agentKeyMaxLen {
		return AgentConfigStatus{}, &agentKeyError{message: "The agent API key is too long"}
	}
	account := agentKeyAccountFor(id)
	if key == "" {
		if err := keys.Delete(credentials.ServiceName, account); err != nil &&
			!errors.Is(err, credentials.ErrNotFound) {
			return AgentConfigStatus{}, &agentKeyError{message: "Failed to clear the agent API key"}
		}
	} else if err := keys.Set(credentials.ServiceName, account, key); err != nil {
		return AgentConfigStatus{}, &agentKeyError{message: "Failed to store the agent API key"}
	}
	return a.agentStatusFrom(current)
}

// AgentSetDefaultModel records the conversation picker's last choice so a new
// SSH tab starts on the same model.
func (a *App) AgentSetDefaultModel(providerID, model string) (AgentConfigStatus, error) {
	if err := a.migrateAgentProviders(); err != nil {
		return AgentConfigStatus{}, err
	}
	s, err := a.settingsStore()
	if err != nil {
		return AgentConfigStatus{}, err
	}
	current, err := s.Get()
	if err != nil {
		return AgentConfigStatus{}, err
	}
	providerID = strings.TrimSpace(providerID)
	model = strings.TrimSpace(model)
	p, ok := findProvider(current.AgentProviders, providerID)
	if !ok || !providerHasModel(p, model) {
		return AgentConfigStatus{}, &agentKeyError{message: "Unknown agent model"}
	}
	if _, err := s.Set(settings.Patch{
		AgentDefaultProviderID: &providerID,
		AgentDefaultModel:      &model,
	}); err != nil {
		return AgentConfigStatus{}, err
	}
	current, err = s.Get()
	if err != nil {
		return AgentConfigStatus{}, err
	}
	return a.agentStatusFrom(current)
}

// agentService returns the wired assistant.
func (a *App) agentService() (*agent.Service, error) {
	a.mu.RLock()
	svc := a.agent
	a.mu.RUnlock()
	if svc == nil {
		return nil, errBackendNotInitialised
	}
	return svc, nil
}

// AgentPrompt accepts one message for the session's conversation. providerID
// and model select which named endpoint this send uses; the transcript is
// kept. It returns an error only when the prompt is rejected up front (not
// configured, unknown model, empty, a run already in flight); an accepted
// run reports progress through the agent:delta/tool/error events and is
// always closed by agent:done. title is the session's tab label, so the
// assistant names the host the way the UI does.
func (a *App) AgentPrompt(sessionID, title, text, providerID, model string) error {
	svc, err := a.agentService()
	if err != nil {
		return err
	}
	cfg, err := a.agentConfigFor(providerID, model)
	if err != nil {
		return err
	}
	return svc.Prompt(sessionID, title, text, cfg)
}

// AgentAbort stops the session's in-flight run; the run still closes with an
// agent:done event marked aborted.
func (a *App) AgentAbort(sessionID string) error {
	svc, err := a.agentService()
	if err != nil {
		return err
	}
	svc.Abort(sessionID)
	return nil
}

// AgentClear stops the in-flight run and drops the session's conversation, so
// the next prompt starts fresh.
func (a *App) AgentClear(sessionID string) error {
	svc, err := a.agentService()
	if err != nil {
		return err
	}
	svc.Clear(sessionID)
	return nil
}

// PermissionDecide answers an in-app permission:ask. Unknown ids are ignored
// (already cancelled). The renderer is the only caller; MCP uses a native
// dialog in its own process and never hits this binding.
func (a *App) PermissionDecide(id, decision string) error {
	a.mu.RLock()
	gate := a.permGate
	a.mu.RUnlock()
	if gate == nil {
		return errBackendNotInitialised
	}
	d, ok := permission.ParseDecision(decision)
	if !ok {
		return &permission.Error{Code: apperror.Unknown, Message: "Unknown permission decision"}
	}
	gate.Decide(id, d)
	return nil
}

// DialogOpenPrivateKeyFile opens the private-key picker. The returned path is
// raw; the home-boundary constraint is enforced later by the credential
// reader when the save payload is processed.
func (a *App) DialogOpenPrivateKeyFile() (string, error) {
	ctx, err := a.dialogCtx()
	if err != nil {
		return "", err
	}
	p, err := openPrivateKeyDialog(ctx)
	if err != nil {
		return "", err
	}
	return p, nil
}

// appVersion is the version shown on the About page. Keep it in sync with
// wails.json info.productVersion and package.json version (enforced by
// TestAppVersionMatchesManifests); a build can override it with
// -ldflags "-X main.appVersion=...".
var appVersion = "2.0.0"

// FontsList returns the system font families for the Settings modal. The
// binding never rejects: a failed enumeration surfaces as an empty list
// (Electron parity), and the value is always a non-nil slice so the frontend
// never sees null. Font enumeration is stateless, so the call works even
// before startup.
func (a *App) FontsList() []string {
	families, err := listFonts(context.Background())
	if err != nil {
		fmt.Fprintf(os.Stderr, "nodeshell: list fonts: %v\n", err)
		return []string{}
	}
	if families == nil {
		return []string{}
	}
	return families
}

// AppGetVersion returns the application version (adapter app.getVersion).
func (a *App) AppGetVersion() string {
	return appVersion
}

// McpRegistrationStatus returns the registration state of all four MCP
// client configs (Cursor, Claude Code, Codex, OpenCode) in UI order. Read
// failures surface as per-target details; the call itself only fails before
// startup (adapter mcpRegistration.status).
func (a *App) McpRegistrationStatus() ([]mcpregistration.TargetStatus, error) {
	a.mu.RLock()
	svc := a.mcpReg
	a.mu.RUnlock()
	if svc == nil {
		return nil, errBackendNotInitialised
	}
	return svc.Status(), nil
}

// McpRegistrationRegister writes the native MCP launcher config (this
// executable with --mcp) into the target's config — or every client's when
// target is "all" — returning one result per target. Targets are merged and
// written independently, so a failure never rolls back the targets already
// written (adapter mcpRegistration.register).
func (a *App) McpRegistrationRegister(target string) ([]mcpregistration.Result, error) {
	a.mu.RLock()
	svc := a.mcpReg
	a.mu.RUnlock()
	if svc == nil {
		return nil, errBackendNotInitialised
	}
	return svc.Register(target)
}

// McpRegistrationClipboardSnippet returns a JSON mcpServers block for manual
// configuration, built from the native spec (adapter
// mcpRegistration.clipboardSnippet). Equivalent to the standard snippet from
// McpRegistrationManualConfig.
func (a *App) McpRegistrationClipboardSnippet() (string, error) {
	a.mu.RLock()
	svc := a.mcpReg
	a.mu.RUnlock()
	if svc == nil {
		return "", errBackendNotInitialised
	}
	return svc.ClipboardSnippet()
}

// McpRegistrationManualConfig returns the native launch spec and paste-ready
// snippets for other MCP clients (adapter mcpRegistration.manualConfig).
func (a *App) McpRegistrationManualConfig() (mcpregistration.ManualConfig, error) {
	a.mu.RLock()
	svc := a.mcpReg
	a.mu.RUnlock()
	if svc == nil {
		return mcpregistration.ManualConfig{}, errBackendNotInitialised
	}
	return svc.ManualConfig()
}

// --- Transfer Manager bindings (ElectronApi.transfer contract) ---

func (a *App) transferManager() (*sftpservice.TransferManager, error) {
	a.mu.RLock()
	tm := a.transfer
	a.mu.RUnlock()
	if tm == nil {
		return nil, errBackendNotInitialised
	}
	return tm, nil
}

// TransferGetTasks returns a snapshot of all active and recent transfer tasks.
func (a *App) TransferGetTasks() ([]sftpservice.TransferTaskDTO, error) {
	tm, err := a.transferManager()
	if err != nil {
		return nil, err
	}
	return tm.GetTasks(), nil
}

// TransferEnqueueUpload enqueues local file paths for upload into remoteDir.
func (a *App) TransferEnqueueUpload(sessionID, remoteDir string, localPaths []string) ([]string, error) {
	tm, err := a.transferManager()
	if err != nil {
		return nil, err
	}
	title := a.resolveSessionTitle(sessionID)
	return tm.EnqueueUpload(sessionID, title, remoteDir, localPaths)
}

// TransferEnqueueDownload enqueues a single file download from remotePath to localPath.
func (a *App) TransferEnqueueDownload(sessionID, remotePath, localPath string) (string, error) {
	tm, err := a.transferManager()
	if err != nil {
		return "", err
	}
	absRemote := a.resolveAbsoluteRemotePath(sessionID, remotePath)
	title := a.resolveSessionTitle(sessionID)
	return tm.EnqueueDownload(sessionID, title, absRemote, localPath)
}

// TransferChooseUploadFiles opens the file chooser dialog and enqueues selected files.
func (a *App) TransferChooseUploadFiles(sessionID, remoteDir string) ([]string, error) {
	tm, err := a.transferManager()
	if err != nil {
		return nil, err
	}
	ctx, err := a.dialogCtx()
	if err != nil {
		return nil, err
	}
	paths, err := openUploadDialog(ctx)
	if err != nil {
		return nil, err
	}
	if len(paths) == 0 {
		return nil, nil // cancelled by user
	}
	title := a.resolveSessionTitle(sessionID)
	return tm.EnqueueUpload(sessionID, title, remoteDir, paths)
}

// TransferChooseDownloadTarget opens the save file dialog and enqueues the download.
func (a *App) TransferChooseDownloadTarget(sessionID, remotePath, defaultName string) (string, error) {
	tm, err := a.transferManager()
	if err != nil {
		return "", err
	}
	ctx, err := a.dialogCtx()
	if err != nil {
		return "", err
	}
	target, err := openSaveDialog(ctx, defaultName)
	if err != nil {
		return "", err
	}
	if target == "" {
		return "", nil // cancelled by user
	}
	absRemote := a.resolveAbsoluteRemotePath(sessionID, remotePath)
	title := a.resolveSessionTitle(sessionID)
	return tm.EnqueueDownload(sessionID, title, absRemote, target)
}

func (a *App) resolveAbsoluteRemotePath(sessionID, remotePath string) string {
	if strings.HasPrefix(remotePath, "/") {
		return path.Clean(remotePath)
	}
	a.mu.RLock()
	sftpSvc := a.sftp
	a.mu.RUnlock()
	if sftpSvc != nil {
		if cwd, err := sftpSvc.Cwd(sessionID); err == nil && cwd != "" {
			return sftpservice.JoinRemote(cwd, remotePath)
		}
	}
	return path.Clean("/" + remotePath)
}

// TransferCancel cancels a queued or running transfer task.
func (a *App) TransferCancel(taskID string) error {
	tm, err := a.transferManager()
	if err != nil {
		return err
	}
	return tm.Cancel(taskID)
}

// TransferRetry retries a failed or cancelled transfer task.
func (a *App) TransferRetry(taskID string) (string, error) {
	tm, err := a.transferManager()
	if err != nil {
		return "", err
	}
	return tm.Retry(taskID)
}

// TransferClear removes a finished task from history.
func (a *App) TransferClear(taskID string) error {
	tm, err := a.transferManager()
	if err != nil {
		return err
	}
	return tm.Clear(taskID)
}

// TransferClearCompleted removes all completed, cancelled, or failed tasks.
func (a *App) TransferClearCompleted() error {
	tm, err := a.transferManager()
	if err != nil {
		return err
	}
	return tm.ClearCompleted()
}

func (a *App) resolveSessionTitle(sessionID string) string {
	a.mu.RLock()
	sessMgr := a.sessions
	hostStore := a.hosts
	a.mu.RUnlock()
	if sessMgr != nil && hostStore != nil {
		if hID, ok := sessMgr.HostID(sessionID); ok {
			if h, ok, _ := hostStore.GetByID(hID); ok && h.Name != "" {
				return h.Name
			}
		}
	}
	return sessionID
}

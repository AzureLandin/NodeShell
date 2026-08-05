package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync"

	"github.com/wailsapp/wails/v2/pkg/runtime"

	"nodeshell/internal/apperror"
	"nodeshell/internal/configdir"
	"nodeshell/internal/credentials"
	"nodeshell/internal/credentials/keyring"
	"nodeshell/internal/fonts"
	"nodeshell/internal/hosts"
	"nodeshell/internal/knownhosts"
	"nodeshell/internal/mcpregistration"
	"nodeshell/internal/monitor"
	"nodeshell/internal/sessions"
	"nodeshell/internal/settings"
	"nodeshell/internal/sftpservice"
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
	monitor  *monitor.Service
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
	app := &App{dataDir: dataDir, hosts: h, settings: s, known: k, creds: c, readKey: readKey, home: home, mcpReg: newMcpRegistration()}
	sink := &disposeSink{next: &wailsSink{}, sftp: func() *sftpservice.Service { return app.sftp }, monitor: func() *monitor.Service { return app.monitor }}
	app.wireSessions(h, k, c, readKey, sink)
	app.sftp = sftpservice.New(sftpservice.Deps{Opener: app.sessions, Sink: sink, Home: home})
	app.wireMonitor(sink)
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

// disposeSink forwards every event to the next sink and, on session:closed,
// disposes the session's cached SFTP client and stops its monitor poller — a
// torn-down SSH session can never leave a stale SFTP channel or a polling
// goroutine behind. The getters resolve lazily so wiring order (sessions
// before sftp/monitor) never matters.
type disposeSink struct {
	next    sessions.EventSink
	sftp    func() *sftpservice.Service
	monitor func() *monitor.Service
}

func (s *disposeSink) Emit(event string, payload any) {
	if event == sessions.EventSessionClosed {
		if e, ok := payload.(sessions.ClosedEvent); ok {
			if svc := s.sftp(); svc != nil {
				svc.Dispose(e.SessionID)
			}
			if m := s.monitor(); m != nil {
				m.Dispose(e.SessionID)
			}
		}
	}
	if s.next != nil {
		s.next.Emit(event, payload)
	}
}

// filesOnDropEvent carries absolute dropped file paths to the frontend
// adapter (payload {"paths": [...]}); the SftpPanel associates them with its
// current session.
const filesOnDropEvent = "files:onDrop"

// registerFileDrop hooks the Wails file-drop callback so dropped files reach
// the frontend as events. Registration is skipped when ctx does not carry a
// Wails runtime (unit tests call startup with a plain context, where
// runtime.OnFileDrop would fatal-exit); the value check mirrors the one
// runtime itself performs.
func (a *App) registerFileDrop(ctx context.Context) {
	if ctx == nil || ctx.Value("events") == nil {
		return
	}
	runtime.OnFileDrop(ctx, func(x, y int, paths []string) {
		if len(paths) == 0 {
			return
		}
		runtime.EventsEmit(ctx, filesOnDropEvent, map[string]any{"paths": paths})
	})
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
	sink := &disposeSink{next: &wailsSink{ctx: ctx}, sftp: func() *sftpservice.Service { return a.sftp }, monitor: func() *monitor.Service { return a.monitor }}
	a.wireSessions(a.hosts, a.known, a.creds, a.readKey, sink)
	a.sftp = sftpservice.New(sftpservice.Deps{Opener: a.sessions, Sink: sink, Home: home})
	a.wireMonitor(sink)
	a.registerFileDrop(ctx)
}

// shutdown is the Wails OnShutdown hook: the monitor poller is stopped and
// joined before the sessions, so no poll is ever mid-exec against a torn-down
// session and no monitor:update event can be emitted while the WebView tears
// down. Sessions and SFTP clients are then disposed quietly.
func (a *App) shutdown(context.Context) {
	a.mu.RLock()
	m := a.sessions
	svc := a.sftp
	mon := a.monitor
	a.mu.RUnlock()
	if mon != nil {
		mon.DisposeAll()
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
// mcpRegistration.clipboardSnippet).
func (a *App) McpRegistrationClipboardSnippet() (string, error) {
	a.mu.RLock()
	svc := a.mcpReg
	a.mu.RUnlock()
	if svc == nil {
		return "", errBackendNotInitialised
	}
	return svc.ClipboardSnippet()
}

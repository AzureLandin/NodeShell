// Package mcpcli owns the native MCP stdio mode entry contract. The GUI and
// MCP modes are mutually exclusive; --mcp must never initialise the WebView.
package mcpcli

import (
	"context"
	"fmt"
	"io"
	"os"
	"time"

	"nodeshell/internal/configdir"
	"nodeshell/internal/credentials"
	"nodeshell/internal/credentials/keyring"
	"nodeshell/internal/hosts"
	"nodeshell/internal/knownhosts"
	"nodeshell/internal/sessions"
	"nodeshell/internal/settings"
	"nodeshell/internal/sftpservice"
)

// WantsMCP reports whether args request MCP stdio mode. The --mcp switch is
// matched exactly; lookalikes (e.g. --mcp-extra) fall through to the GUI.
func WantsMCP(args []string) bool {
	for _, arg := range args {
		if arg == "--mcp" {
			return true
		}
	}
	return false
}

// Run serves the MCP stdio protocol on in/out over a runtime wired from deps,
// disposing every session before returning. It returns nil on a clean EOF or
// when ctx is cancelled; a framing or I/O error is returned. Diagnostics go
// to errOut, never to out. The caller owns deps.Manager: the SessionManager
// interface exposes no shutdown surface, so a real manager must be disposed
// by whoever built it (production RunMCP does).
func Run(ctx context.Context, deps Deps, in io.Reader, out io.Writer, errOut io.Writer) error {
	rt := New(deps)
	defer rt.DisposeAll()
	return NewServer(rt, out, errOut).Serve(ctx, in)
}

// ManagerShutdown is the teardown surface the MCP process needs on its
// session manager: cancel every in-flight connect and close every session
// (*sessions.Manager satisfies it).
type ManagerShutdown interface {
	CancelConnect()
	DisposeAll()
}

// mcpManager is the full manager surface RunMCP wires: the runtime's
// SessionManager plus the shutdown contract. *sessions.Manager satisfies it;
// kept as an interface so tests can inject a fake end to end.
type mcpManager interface {
	SessionManager
	ManagerShutdown
}

// shutdownMCP tears the MCP wiring down in dependency order. The manager
// goes first: DisposeAll cancels in-flight connects (releasing a connect
// blocked in the network layer) and quietly closes every session, and its
// closing gate guarantees no session can be inserted afterwards. The runtime
// then stops and joins the reaper and drops the remaining session metadata;
// its per-session manager.Disconnect on the already-disposed manager is a
// no-op. Callers own the manager: production RunMCP builds it and defers
// this before Serve, so a clean EOF and a cancelled ctx (SIGINT) take the
// same path.
func shutdownMCP(rt *Runtime, m ManagerShutdown) {
	m.DisposeAll()
	rt.DisposeAll()
}

// OS seams for RunMCP (test injection), mirroring the app.go seam pattern so
// the production entry is testable without touching the real user profile.
var (
	resolveDataDir    = configdir.DataDir
	userHomeDir       = os.UserHomeDir
	newCredentials    = func() *credentials.Store { return credentials.New(keyring.NewBackend()) }
	newSessionManager = func(d sessions.Deps) mcpManager { return sessions.New(d) }
)

// RunMCP is the production --mcp entry: it resolves the OS data dir, wires
// the stores, an independent session manager (never the GUI's), the SFTP
// service and the idle reaper, then serves the stdio protocol until EOF or an
// interrupt. The MCP session policy (max sessions, idle timeout) comes from
// the user settings with the product defaults as fallback.
func RunMCP(ctx context.Context, in io.Reader, out io.Writer, errOut io.Writer) error {
	dir, err := resolveDataDir()
	if err != nil {
		return fmt.Errorf("nodeshell: resolve data dir: %w", err)
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("nodeshell: create data dir: %w", err)
	}
	home, err := userHomeDir()
	if err != nil {
		return fmt.Errorf("nodeshell: resolve user home: %w", err)
	}

	h := hosts.New(dir)
	k := knownhosts.New(dir)
	if err := k.Load(); err != nil {
		fmt.Fprintf(errOut, "nodeshell: load known hosts: %v\n", err)
	}

	maxSessions, idleTimeout := DefaultMaxSessions, DefaultIdleTimeout
	if st, err := settings.New(dir).Get(); err == nil {
		maxSessions = st.McpMaxSessions
		idleTimeout = time.Duration(st.McpIdleTimeoutMinutes) * time.Minute
	} else {
		fmt.Fprintf(errOut, "nodeshell: read settings: %v\n", err)
	}

	rt := New(Deps{Hosts: h, MaxSessions: maxSessions, IdleTimeout: idleTimeout})
	m := newSessionManager(sessions.Deps{
		Hosts:    h,
		HostKeys: k,
		Creds:    newCredentials(),
		ReadKey:  credentials.NewHomeReader(home),
		Sink:     rt.Sink(),
	})
	rt.SetManager(m)
	rt.SetSFTP(sftpservice.New(sftpservice.Deps{Opener: m, Home: home}))
	rt.StartReaper(0)
	defer shutdownMCP(rt, m)
	return NewServer(rt, out, errOut).Serve(ctx, in)
}

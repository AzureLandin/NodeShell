package mcpcli

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"nodeshell/internal/apperror"
	"nodeshell/internal/credentials"
	"nodeshell/internal/sessions"
)

// TestShutdownMCP (I2 RED): shutdownMCP must dispose the session manager
// first — releasing an in-flight connect and closing every session — then
// close the runtime, so nothing survives the MCP process teardown. The old
// RunMCP deferred only rt.DisposeAll, so the manager's in-flight connects
// and sessions were never cancelled/disposed by the runtime contract.
func TestShutdownMCP(t *testing.T) {
	m := newFakeManager()
	m.connectStart = make(chan struct{}, 1)
	m.connectBlock = make(chan struct{})
	m.connectErr = &sessions.Error{Code: apperror.Cancelled, Message: "cancelled"}
	rt := newTestRuntime(4, time.Minute, m, &fakeSFTP{}, nil)

	connectDone := make(chan struct{})
	var connectErr error
	go func() {
		defer close(connectDone)
		_, connectErr = rt.ConnectHost(context.Background(), "h1", sessions.ConnectOptions{})
	}()
	<-m.connectStart // the connect is in flight and blocked

	shutdownMCP(rt, m)

	select {
	case <-connectDone:
	case <-time.After(5 * time.Second):
		t.Fatal("shutdownMCP did not release the blocked connect")
	}
	assertErrorCode(t, connectErr, apperror.Cancelled)
	m.mu.Lock()
	called := m.cancelConnectCalled && m.disposeAllCalled
	m.mu.Unlock()
	if !called {
		t.Fatal("shutdownMCP must dispose the session manager")
	}
	if got := rt.ListSessions(); len(got) != 0 {
		t.Fatalf("sessions survive shutdownMCP: %+v", got)
	}
	// The runtime is closed: a later connect is refused before the manager.
	if _, err := rt.ConnectHost(context.Background(), "h1", sessions.ConnectOptions{}); err == nil {
		t.Fatal("connect after shutdownMCP must be refused")
	} else {
		assertErrorCode(t, err, apperror.Cancelled)
	}
}

// TestRunMCPCtxCancelShutsDown (I2 RED): cancelling the ctx (SIGINT) while a
// connect_host call is in flight must release the connect, return RunMCP
// promptly and dispose the session manager — the process must not hang on
// shutdown.
func TestRunMCPCtxCancelShutsDown(t *testing.T) {
	dir := t.TempDir()
	hostsJSON := `{"hosts":[{"id":"h1","name":"lab","host":"192.0.2.1","port":22,"username":"user","authMethod":"password","credentialsSaved":false}]}`
	if err := os.WriteFile(filepath.Join(dir, "hosts.json"), []byte(hostsJSON), 0o600); err != nil {
		t.Fatalf("write hosts fixture: %v", err)
	}

	oldDir, oldHome, oldCreds, oldMgr := resolveDataDir, userHomeDir, newCredentials, newSessionManager
	t.Cleanup(func() {
		resolveDataDir, userHomeDir, newCredentials, newSessionManager = oldDir, oldHome, oldCreds, oldMgr
	})
	resolveDataDir = func() (string, error) { return dir, nil }
	userHomeDir = func() (string, error) { return t.TempDir(), nil }
	newCredentials = func() *credentials.Store { return credentials.New(nopCredBackend{}) }

	m := newFakeManager()
	m.connectStart = make(chan struct{}, 1)
	m.connectBlock = make(chan struct{})
	newSessionManager = func(sessions.Deps) mcpManager { return m }

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	input := strings.Join([]string{
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05","clientInfo":{"name":"smoke","version":"1"},"capabilities":{}}}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"connect_host","arguments":{"hostId":"h1"}}}`,
	}, "\n") + "\n"

	var out, errOut bytes.Buffer
	runDone := make(chan struct{})
	var runErr error
	go func() {
		defer close(runDone)
		runErr = RunMCP(ctx, strings.NewReader(input), &out, &errOut)
	}()

	<-m.connectStart // the connect_host call is in flight and blocked
	cancel()

	select {
	case <-runDone:
	case <-time.After(5 * time.Second):
		t.Fatal("RunMCP did not return after ctx cancellation")
	}
	if runErr != nil {
		t.Fatalf("RunMCP: %v", runErr)
	}
	m.mu.Lock()
	shutdown := m.cancelConnectCalled && m.disposeAllCalled
	m.mu.Unlock()
	if !shutdown {
		t.Fatal("RunMCP did not dispose the session manager on ctx cancellation")
	}
}

package main

import (
	"context"
	"errors"
	"strings"
	"testing"

	"nodeshell/internal/apperror"
	"nodeshell/internal/hosts"
	"nodeshell/internal/sessions"
	"nodeshell/internal/sshtest"
)

// TestSessionsBindingsUninitialised: every sessions binding must fail
// observably (never fake success) on an App that was never started.
func TestSessionsBindingsUninitialised(t *testing.T) {
	a := NewApp()
	if _, err := a.SessionsConnect("h", sessions.ConnectOptions{}); err == nil {
		t.Fatal("SessionsConnect on uninitialised App must error")
	}
	if err := a.SessionsWrite("s", "x"); err == nil {
		t.Fatal("SessionsWrite on uninitialised App must error")
	}
	if err := a.SessionsResize("s", 80, 24); err == nil {
		t.Fatal("SessionsResize on uninitialised App must error")
	}
	if err := a.SessionsDisconnect("s"); err == nil {
		t.Fatal("SessionsDisconnect on uninitialised App must error")
	}
	if err := a.SessionsCancelConnect(); err == nil {
		t.Fatal("SessionsCancelConnect on uninitialised App must error")
	}
}

// TestSessionsConnectUnknownHost: an unknown host id is an observable
// HOST_NOT_FOUND error, never a fake session.
func TestSessionsConnectUnknownHost(t *testing.T) {
	a, _ := testApp(t)
	_, err := a.SessionsConnect("does-not-exist", sessions.ConnectOptions{})
	if err == nil {
		t.Fatal("SessionsConnect of unknown host must error")
	}
	var se *sessions.Error
	if !errors.As(err, &se) || se.Code != apperror.HostNotFound {
		t.Fatalf("error = %v, want sessions.Error with code %s", err, apperror.HostNotFound)
	}
}

// TestSessionsConnectAndDisconnectIntegration drives the full binding chain
// against a real local SSH server: create host, seed the host key, connect,
// write (echo round-trip) and disconnect.
func TestSessionsConnectAndDisconnectIntegration(t *testing.T) {
	srv := sshtest.New(t)
	srv.PasswordOK = func(user, pass string) bool { return user == "user" && pass == "secret" }
	host, port := splitAddr(t, srv.Addr)

	a, _ := testApp(t)
	created, err := a.HostsCreate(hosts.HostInput{
		Name: "lab", Host: host, Port: port, Username: "user", AuthMethod: "password",
	})
	if err != nil {
		t.Fatalf("HostsCreate: %v", err)
	}
	if err := a.known.Remember(host, port, srv.HostKeyFingerprint()); err != nil {
		t.Fatalf("Remember host key: %v", err)
	}

	res, err := a.SessionsConnect(created.Id, sessions.ConnectOptions{Password: "secret"})
	if err != nil {
		t.Fatalf("SessionsConnect: %v", err)
	}
	if res.SessionID == "" {
		t.Fatal("SessionsConnect must return a session id")
	}
	if err := a.SessionsWrite(res.SessionID, "hi\n"); err != nil {
		t.Fatalf("SessionsWrite: %v", err)
	}
	if err := a.SessionsResize(res.SessionID, 100, 40); err != nil {
		t.Fatalf("SessionsResize: %v", err)
	}
	if err := a.SessionsDisconnect(res.SessionID); err != nil {
		t.Fatalf("SessionsDisconnect: %v", err)
	}
	if err := a.SessionsDisconnect(res.SessionID); err != nil {
		t.Fatalf("second Disconnect: %v", err)
	}
	// Unknown session: disconnect stays a no-op success (Electron parity).
	if err := a.SessionsDisconnect("nope"); err != nil {
		t.Fatalf("Disconnect of unknown session: %v", err)
	}
}

// TestSessionsShutdownDisposes: shutdown (OnShutdown) disposes every session
// quietly; a write afterwards is SESSION_NOT_FOUND.
func TestSessionsShutdownDisposes(t *testing.T) {
	srv := sshtest.New(t)
	srv.PasswordOK = func(user, pass string) bool { return true }
	host, port := splitAddr(t, srv.Addr)

	a, _ := testApp(t)
	created, err := a.HostsCreate(hosts.HostInput{
		Name: "lab", Host: host, Port: port, Username: "user", AuthMethod: "password",
	})
	if err != nil {
		t.Fatalf("HostsCreate: %v", err)
	}
	if err := a.known.Remember(host, port, srv.HostKeyFingerprint()); err != nil {
		t.Fatalf("Remember: %v", err)
	}
	res, err := a.SessionsConnect(created.Id, sessions.ConnectOptions{Password: "secret"})
	if err != nil {
		t.Fatalf("SessionsConnect: %v", err)
	}
	a.shutdown(context.Background())
	if err := a.SessionsWrite(res.SessionID, "x"); err == nil {
		t.Fatal("SessionsWrite after shutdown must error (session disposed)")
	}
}

// TestSessionsCancelConnectBinding: cancelConnect is available and no-ops when
// nothing is in flight (no panic, no error).
func TestSessionsCancelConnectBinding(t *testing.T) {
	a, _ := testApp(t)
	if err := a.SessionsCancelConnect(); err != nil {
		t.Fatalf("SessionsCancelConnect with no in-flight connect: %v", err)
	}
}

// splitAddr splits "host:port" into host and numeric port.
func splitAddr(t *testing.T, addr string) (string, int) {
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

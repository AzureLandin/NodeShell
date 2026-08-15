package main

import (
	"net"
	"strconv"
	"testing"
	"time"

	"nodeshell/internal/sessions"
	"nodeshell/internal/tunnel"
)

func TestTunnelsUninitialised(t *testing.T) {
	a := NewApp()
	if _, err := a.TunnelsDiscover("s1"); err == nil {
		t.Fatal("TunnelsDiscover on uninitialised App must error")
	}
	if _, err := a.TunnelsStart("s1", "127.0.0.1", 80); err == nil {
		t.Fatal("TunnelsStart on uninitialised App must error")
	}
	if err := a.TunnelsStop("s1", "t1"); err == nil {
		t.Fatal("TunnelsStop on uninitialised App must error")
	}
	if _, err := a.TunnelsList("s1"); err == nil {
		t.Fatal("TunnelsList on uninitialised App must error")
	}
}

func TestDisposeSinkDisposesTunnelsOnSessionClose(t *testing.T) {
	svc := tunnel.New(tunnel.Deps{
		Dialer: tunnelDialer{},
		Ready:  tunnelReady{},
	})
	ds := &disposeSink{
		next:    &monCountSink{},
		tunnels: func() *tunnel.Service { return svc },
	}
	tun, err := svc.Start("s1", "127.0.0.1", 1)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	ds.Emit(sessions.EventSessionClosed, sessions.ClosedEvent{SessionID: "s1"})
	if n := len(svc.List("s1")); n != 0 {
		t.Fatalf("List after session:closed = %d", n)
	}
	conn, err := net.DialTimeout("tcp", net.JoinHostPort(tun.LocalHost, strconv.Itoa(tun.LocalPort)), 300*time.Millisecond)
	if err == nil {
		_ = conn.Close()
		t.Fatal("listener still accepted after session:closed")
	}
}

type tunnelDialer struct{}

func (tunnelDialer) Dial(string, string, string) (net.Conn, error) {
	return nil, errBackendNotInitialised
}

type tunnelReady struct{}

func (tunnelReady) CanDial(string) error { return nil }

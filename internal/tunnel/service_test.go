package tunnel

import (
	"bytes"
	"context"
	"io"
	"net"
	"strconv"
	"sync"
	"testing"
	"time"

	"nodeshell/internal/apperror"
)

type staticExecer struct {
	out string
	err error
}

func (e staticExecer) Exec(string, context.Context, string, time.Duration) (string, error) {
	return e.out, e.err
}

type echoDialer struct {
	target string
}

func (d echoDialer) Dial(_, _, _ string) (net.Conn, error) {
	return net.Dial("tcp", d.target)
}

type errDialer struct{ err error }

func (d errDialer) Dial(string, string, string) (net.Conn, error) { return nil, d.err }

type readyOK struct{}

func (readyOK) CanDial(string) error { return nil }

type readyErr struct{ err error }

func (r readyErr) CanDial(string) error { return r.err }

func echoServer(t *testing.T) (addr string, closeFn func()) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("echo listen: %v", err)
	}
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func(conn net.Conn) {
				defer conn.Close()
				_, _ = io.Copy(conn, conn)
			}(c)
		}
	}()
	return ln.Addr().String(), func() {
		_ = ln.Close()
		wg.Wait()
	}
}

func TestDiscoverParsesExecOutput(t *testing.T) {
	svc := New(Deps{Execer: staticExecer{out: "LISTEN 0 128 0.0.0.0:8080 0.0.0.0:*\n"}})
	got, err := svc.Discover(context.Background(), "s1")
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if len(got) != 1 || got[0].Port != 8080 || got[0].Bind != "0.0.0.0" {
		t.Fatalf("Discover = %#v", got)
	}
}

func TestStartForwardsBytes(t *testing.T) {
	addr, closeEcho := echoServer(t)
	defer closeEcho()

	svc := New(Deps{
		Dialer: echoDialer{target: addr},
		Ready:  readyOK{},
		UUID:   func() string { return "tun-1" },
	})
	defer svc.DisposeAll()

	tun, err := svc.Start("s1", "127.0.0.1", 18080)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if tun.ID != "tun-1" || tun.LocalHost != "127.0.0.1" || tun.LocalPort <= 0 {
		t.Fatalf("Start tunnel = %+v", tun)
	}

	conn, err := net.Dial("tcp", net.JoinHostPort(tun.LocalHost, strconv.Itoa(tun.LocalPort)))
	if err != nil {
		t.Fatalf("dial local: %v", err)
	}
	defer conn.Close()
	payload := []byte("hello-tunnel")
	if _, err := conn.Write(payload); err != nil {
		t.Fatalf("write: %v", err)
	}
	buf := make([]byte, len(payload))
	if err := conn.SetReadDeadline(time.Now().Add(2 * time.Second)); err != nil {
		t.Fatalf("deadline: %v", err)
	}
	if _, err := io.ReadFull(conn, buf); err != nil {
		t.Fatalf("read: %v", err)
	}
	if !bytes.Equal(buf, payload) {
		t.Fatalf("echo = %q, want %q", buf, payload)
	}
}

func TestStartFallsBackWhenPortBusy(t *testing.T) {
	busy, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("busy listen: %v", err)
	}
	defer busy.Close()
	busyPort := busy.Addr().(*net.TCPAddr).Port

	svc := New(Deps{
		Dialer: errDialer{err: errf(apperror.Unknown, "unused")},
		Ready:  readyOK{},
	})
	defer svc.DisposeAll()

	tun, err := svc.Start("s1", "127.0.0.1", busyPort)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if tun.LocalPort == busyPort {
		t.Fatalf("Start reused busy local port %d", busyPort)
	}
	if tun.RemotePort != busyPort {
		t.Fatalf("RemotePort = %d, want %d", tun.RemotePort, busyPort)
	}
}

func TestStartReusesExisting(t *testing.T) {
	svc := New(Deps{
		Dialer: errDialer{err: errf(apperror.Unknown, "unused")},
		Ready:  readyOK{},
		UUID: func() string {
			return "same"
		},
	})
	defer svc.DisposeAll()
	a, err := svc.Start("s1", "0.0.0.0", 9999)
	if err != nil {
		t.Fatalf("Start 1: %v", err)
	}
	b, err := svc.Start("s1", "0.0.0.0", 9999)
	if err != nil {
		t.Fatalf("Start 2: %v", err)
	}
	if a.ID != b.ID || a.LocalPort != b.LocalPort {
		t.Fatalf("reuse = %+v vs %+v", a, b)
	}
	if n := len(svc.List("s1")); n != 1 {
		t.Fatalf("List = %d, want 1", n)
	}
}

func TestDisposeClosesListener(t *testing.T) {
	svc := New(Deps{
		Dialer: errDialer{err: errf(apperror.Unknown, "unused")},
		Ready:  readyOK{},
	})
	tun, err := svc.Start("s1", "127.0.0.1", 1)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	svc.Dispose("s1")
	if n := len(svc.List("s1")); n != 0 {
		t.Fatalf("List after Dispose = %d", n)
	}
	conn, err := net.DialTimeout("tcp", net.JoinHostPort(tun.LocalHost, strconv.Itoa(tun.LocalPort)), 300*time.Millisecond)
	if err == nil {
		_ = conn.Close()
		t.Fatal("listener still accepted after Dispose")
	}
}

func TestStopUnknownIsNoop(t *testing.T) {
	svc := New(Deps{Dialer: errDialer{err: nil}, Ready: readyOK{}})
	if err := svc.Stop("s1", "missing"); err != nil {
		t.Fatalf("Stop unknown: %v", err)
	}
}

func TestStartRejectsWhenNotReady(t *testing.T) {
	want := errf(apperror.SessionNotFound, "Session not found: s1")
	svc := New(Deps{Dialer: errDialer{err: nil}, Ready: readyErr{err: want}})
	_, err := svc.Start("s1", "127.0.0.1", 80)
	if err != want {
		t.Fatalf("Start = %v, want session-not-found", err)
	}
}

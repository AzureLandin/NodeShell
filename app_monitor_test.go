package main

import (
	"context"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"

	"nodeshell/internal/hosts"
	"nodeshell/internal/monitor"
	"nodeshell/internal/sessions"
	"nodeshell/internal/sftpservice"
	"nodeshell/internal/sshtest"
)

// monSampleOutput is a valid monitor script result returned by the test SSH
// server for every exec.
const monSampleOutput = `---STAT---
cpu  10 0 5 85 0 0 0 0
---MEM---
MemTotal:        1000000 kB
MemAvailable:     400000 kB
SwapTotal:             0 kB
SwapFree:              0 kB
---LOAD---
1.00 2.00 3.00 1/1 1
---NET---
Inter-|   Receive                                                |  Transmit
 face |bytes    packets errs drop fifo frame compressed multicast|bytes    packets errs drop fifo colls carrier compressed
  eth0: 100 0 0 0 0 0 0 0 200 0 0 0 0 0 0 0
---PS---
91234  1.7 YDService
`

func TestMonitorSetActiveUninitialised(t *testing.T) {
	a := NewApp()
	if err := a.MonitorSetActive("", ""); err == nil {
		t.Fatal("MonitorSetActive on uninitialised App must error")
	}
	if err := a.MonitorSetActive("s", "title"); err == nil {
		t.Fatal("MonitorSetActive(session) on uninitialised App must error")
	}
}

// monStaticExecer answers every exec with the sample output.
type monStaticExecer struct{}

func (monStaticExecer) Exec(_ string, _ context.Context, _ string, _ time.Duration) (string, error) {
	return monSampleOutput, nil
}

// monCountSink counts emitted events.
type monCountSink struct {
	mu sync.Mutex
	n  int
}

func (s *monCountSink) Emit(string, any) {
	s.mu.Lock()
	s.n++
	s.mu.Unlock()
}

func (s *monCountSink) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.n
}

// TestDisposeSinkDisposesMonitorOnSessionClose proves the dispose wiring: a
// session:closed event synchronously stops the monitor poller of that session
// (only the matching session) and is forwarded to the next sink.
func TestDisposeSinkDisposesMonitorOnSessionClose(t *testing.T) {
	monSink := &monCountSink{}
	svc := monitor.New(monitor.Deps{Execer: monStaticExecer{}, Sink: monSink, Interval: 10 * time.Millisecond})
	next := &monCountSink{}
	ds := &disposeSink{
		next:    next,
		sftp:    func() *sftpservice.Service { return nil },
		monitor: func() *monitor.Service { return svc },
	}

	svc.SetActive("s1", "")
	waitCount(t, monSink, 2, "monitor events before close")
	ds.Emit(sessions.EventSessionClosed, sessions.ClosedEvent{SessionID: "s1"})
	before := monSink.count()
	time.Sleep(50 * time.Millisecond)
	if got := monSink.count(); got != before {
		t.Fatalf("poller kept running after session:closed: %d -> %d", before, got)
	}
	if next.count() == 0 {
		t.Fatal("session:closed must be forwarded to the next sink")
	}

	// A close for a different session must not stop the poller.
	svc.SetActive("s2", "")
	waitCount(t, monSink, before+2, "events after re-activate")
	ds.Emit(sessions.EventSessionClosed, sessions.ClosedEvent{SessionID: "other"})
	waitCount(t, monSink, before+4, "events after non-matching close")
	svc.DisposeAll()
}

// waitCount polls until the sink reaches n events.
func waitCount(t *testing.T, s *monCountSink, n int, what string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		if s.count() >= n {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s (%d of %d)", what, s.count(), n)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// TestMonitorBindingPollsRealSession drives the full chain: MonitorSetActive
// → monitor service → sessions.Manager.Exec → real SSH exec with the
// base64-wrapped script, and proves shutdown/disconnect stop the polling. The
// exec handler holds the in-flight poll open, so the synchronous proof that
// session close cancels it (the handler only returns when the client tears
// the exec channel down) is a barrier, not a timing window.
func TestMonitorBindingPollsRealSession(t *testing.T) {
	srv := sshtest.New(t)
	srv.PasswordOK = func(user, pass string) bool { return user == "user" && pass == "secret" }
	var execCount atomic.Int64
	var execMu sync.Mutex
	var execCommands []string
	execClosed := make(chan struct{})
	var closeOnce sync.Once
	srv.OnExec = func(ch ssh.Channel, reqs <-chan *ssh.Request, command string) {
		execMu.Lock()
		execCount.Add(1)
		execCommands = append(execCommands, command)
		execMu.Unlock()
		// Block until the client cancels or tears down the exec channel: a
		// cancelled in-flight poll unblocks here, which is the synchronous
		// evidence that session close disposed the monitor.
		for range reqs {
		}
		closeOnce.Do(func() { close(execClosed) })
		_ = ch.Close()
	}
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
	t.Cleanup(func() { a.shutdown(context.Background()) })

	if err := a.MonitorSetActive(res.SessionID, "my title"); err != nil {
		t.Fatalf("MonitorSetActive: %v", err)
	}
	// The immediate tick must exec the base64-wrapped script over the session.
	waitTrueApp(t, "monitor exec lands", func() bool { return execCount.Load() >= 1 })
	execMu.Lock()
	cmd := ""
	if len(execCommands) > 0 {
		cmd = execCommands[0]
	}
	execMu.Unlock()
	if !strings.HasPrefix(cmd, "echo ") || !strings.Contains(cmd, " | base64 -d | /bin/sh") {
		t.Fatalf("exec command %q is not the base64-wrapped monitor script", cmd)
	}

	// Disconnect the session: the session:closed sink must dispose the
	// monitor, cancelling the in-flight exec (the handler only returns once
	// the client tears the exec channel down) and issuing no further execs.
	if err := a.SessionsDisconnect(res.SessionID); err != nil {
		t.Fatalf("SessionsDisconnect: %v", err)
	}
	select {
	case <-execClosed:
	case <-time.After(5 * time.Second):
		t.Fatal("in-flight monitor exec was not cancelled when the session closed")
	}
	time.Sleep(150 * time.Millisecond)
	if got := execCount.Load(); got != 1 {
		t.Fatalf("monitor issued %d execs after session close, want 1 (disposed poller)", got)
	}

	// Empty session clears (no error); the monitor is already disposed.
	if err := a.MonitorSetActive("", ""); err != nil {
		t.Fatalf("MonitorSetActive(clear): %v", err)
	}

	// Shutdown stays quiet.
	a.shutdown(context.Background())
	time.Sleep(30 * time.Millisecond)
}

// waitTrueApp polls cond until it holds or the deadline passes.
func waitTrueApp(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		if cond() {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("condition never became true: %s", what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

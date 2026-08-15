package mcpcli

import (
	"context"
	"sync"
	"testing"
	"time"

	"nodeshell/internal/apperror"
	"nodeshell/internal/sftpservice"
)

// ctxBlockSFTP is a deterministic fake SFTP service whose operations park
// until the session's SFTP client is disposed: Dispose releases every parked
// call for the session and makes later calls return immediately — exactly
// what closing a real pkg/sftp client does. started()/finished() count every
// parked call, so started()==finished() proves every worker goroutine
// settled.
type ctxBlockSFTP struct {
	inner SFTP

	mu          sync.Mutex
	released    map[string]bool
	blocked     map[string][]chan struct{}
	disposed    []string
	interrupted []string
	in, out     int
}

func newCtxBlockSFTP(inner SFTP) *ctxBlockSFTP {
	return &ctxBlockSFTP{inner: inner, released: map[string]bool{}, blocked: map[string][]chan struct{}{}}
}

// park blocks one operation call until the session's client is disposed; a
// call that starts after the dispose returns immediately (the client is
// already closed).
func (s *ctxBlockSFTP) park(sessionID string) {
	s.mu.Lock()
	s.in++
	released := s.released[sessionID]
	var unblock chan struct{}
	if !released {
		unblock = make(chan struct{})
		s.blocked[sessionID] = append(s.blocked[sessionID], unblock)
	}
	s.mu.Unlock()
	if !released {
		<-unblock
	}
	s.mu.Lock()
	s.out++
	s.mu.Unlock()
}

func (s *ctxBlockSFTP) Chdir(sessionID, remotePath string) (string, error) {
	s.park(sessionID)
	return s.inner.Chdir(sessionID, remotePath)
}

func (s *ctxBlockSFTP) Cwd(sessionID string) (string, error) {
	s.park(sessionID)
	return s.inner.Cwd(sessionID)
}

func (s *ctxBlockSFTP) List(sessionID, remotePath string) ([]sftpservice.Entry, error) {
	s.park(sessionID)
	return s.inner.List(sessionID, remotePath)
}

func (s *ctxBlockSFTP) ReadText(sessionID, remotePath string, maxBytes int64) (string, string, error) {
	s.park(sessionID)
	return s.inner.ReadText(sessionID, remotePath, maxBytes)
}

func (s *ctxBlockSFTP) WriteText(sessionID, remotePath, content string, maxBytes int64) (string, error) {
	s.park(sessionID)
	return s.inner.WriteText(sessionID, remotePath, content, maxBytes)
}

func (s *ctxBlockSFTP) UploadAs(sessionID, localPath, remoteName string) error {
	s.park(sessionID)
	return s.inner.UploadAs(sessionID, localPath, remoteName)
}

func (s *ctxBlockSFTP) Download(sessionID, remotePath, localPath string) error {
	s.park(sessionID)
	return s.inner.Download(sessionID, remotePath, localPath)
}

// Dispose releases every parked call for the session — closing the real
// client's in-flight requests — records the dispose and forwards to the
// inner fake.
func (s *ctxBlockSFTP) Dispose(sessionID string) {
	s.mu.Lock()
	chans := s.blocked[sessionID]
	s.blocked[sessionID] = nil
	s.released[sessionID] = true
	s.disposed = append(s.disposed, sessionID)
	s.mu.Unlock()
	for _, ch := range chans {
		close(ch)
	}
	s.inner.Dispose(sessionID)
}

// Interrupt unblocks parked calls the same way closing a client would, but
// does not Dispose the inner handle — guest cancel must leave GUI cwd intact.
// released is set so the rest of the worker (e.g. List after Cwd) does not
// park again on the now-closed client.
func (s *ctxBlockSFTP) Interrupt(sessionID string) {
	s.mu.Lock()
	chans := s.blocked[sessionID]
	s.blocked[sessionID] = nil
	s.released[sessionID] = true
	s.interrupted = append(s.interrupted, sessionID)
	s.mu.Unlock()
	for _, ch := range chans {
		close(ch)
	}
	s.inner.Interrupt(sessionID)
}

func (s *ctxBlockSFTP) started() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.in
}

func (s *ctxBlockSFTP) finished() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.out
}

func (s *ctxBlockSFTP) disposedFor(sessionID string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, id := range s.disposed {
		if id == sessionID {
			return true
		}
	}
	return false
}

func (s *ctxBlockSFTP) interruptedFor(sessionID string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, id := range s.interrupted {
		if id == sessionID {
			return true
		}
	}
	return false
}

// TestSftpToolsCancelUnblocksInFlight (I1 RED): a cancelled ctx must unblock
// every in-flight SFTP tool by disposing the session's SFTP client, join the
// worker goroutine and return coded CANCELLED, with the busy count released.
// The old implementation ran pkg/sftp synchronously with no context hook, so
// a blocked operation never returned and the serve loop never saw the
// cancel.
func TestSftpToolsCancelUnblocksInFlight(t *testing.T) {
	clk := newSettableClock()
	m := newFakeManager()
	block := newCtxBlockSFTP(&fakeSFTP{})
	rt := newTestRuntime(4, time.Minute, m, block, clk.now)
	sid := connectOK(t, rt, "h1")

	cases := []struct {
		name string
		tool string
		args func(sid string) map[string]any
	}{
		{"sftp_list", "sftp_list", func(sid string) map[string]any { return map[string]any{"sessionId": sid, "path": ""} }},
		{"sftp_read", "sftp_read", func(sid string) map[string]any { return map[string]any{"sessionId": sid, "path": "a.txt"} }},
		{"sftp_write", "sftp_write", func(sid string) map[string]any {
			return map[string]any{"sessionId": sid, "path": "a.txt", "content": "x"}
		}},
		{"sftp_upload", "sftp_upload", func(sid string) map[string]any {
			return map[string]any{"sessionId": sid, "localPath": "/home/me/a.txt"}
		}},
		{"sftp_download", "sftp_download", func(sid string) map[string]any {
			return map[string]any{"sessionId": sid, "remotePath": "a.txt", "localPath": "/home/me/a.txt"}
		}},
	}
	for _, tc := range cases {
		ctx, cancel := context.WithCancel(context.Background())
		callDone := make(chan struct{})
		var callErr error
		args := tc.args(sid)
		go func() {
			defer close(callDone)
			_, callErr = rt.Call(ctx, tc.tool, args)
		}()
		deadline := time.Now().Add(5 * time.Second)
		for block.started() == 0 {
			if time.Now().After(deadline) {
				t.Fatalf("%s: operation never started", tc.name)
			}
			time.Sleep(time.Millisecond)
		}
		cancel()
		select {
		case <-callDone:
		case <-time.After(5 * time.Second):
			t.Fatalf("%s: Call did not return after the ctx was cancelled (in-flight op never unblocked)", tc.name)
		}
		assertErrorCode(t, callErr, apperror.Cancelled)
		if !block.disposedFor(sid) {
			t.Fatalf("%s: the session's SFTP client was not disposed", tc.name)
		}
		if block.started() != block.finished() {
			t.Fatalf("%s: worker calls not settled: started %d finished %d", tc.name, block.started(), block.finished())
		}
		// Busy count released: the idle session is reapable again.
		clk.advance(time.Minute)
		closed := rt.Reap(clk.now())
		if len(closed) != 1 || closed[0] != sid {
			t.Fatalf("%s: session not reaped after the cancelled op (busy count stuck?): %v", tc.name, closed)
		}
		sid = connectOK(t, rt, "h1")
	}
}

// TestGuestSftpCancelDoesNotDisposeGUIHandle: the in-app Agent shares the GUI
// SFTP service. Cancelling a guest sftp_list/read/write must Interrupt the
// cached client (so pkg/sftp unblocks) and must not Dispose the handle, or
// the panel's cwd would snap back to remote home.
func TestGuestSftpCancelDoesNotDisposeGUIHandle(t *testing.T) {
	cases := []struct {
		name string
		tool string
		args map[string]any
	}{
		{"sftp_list", "sftp_list", map[string]any{"sessionId": "gui-1", "path": "apache2", "chdir": false}},
		{"sftp_read", "sftp_read", map[string]any{"sessionId": "gui-1", "path": "syslog"}},
		{"sftp_write", "sftp_write", map[string]any{"sessionId": "gui-1", "path": "out.txt", "content": "x"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := newFakeManager()
			m.live["gui-1"] = true
			inner := &fakeSFTP{cwd: "/var/log"}
			block := newCtxBlockSFTP(inner)
			rt := New(Deps{
				Hosts:         newFakeHostStore(testHost("h1", "lab")),
				Manager:       m,
				SFTP:          block,
				GuestSessions: true,
			})

			ctx, cancel := context.WithCancel(context.Background())
			callDone := make(chan struct{})
			var callErr error
			go func() {
				defer close(callDone)
				_, callErr = rt.Call(ctx, tc.tool, tc.args)
			}()
			deadline := time.Now().Add(5 * time.Second)
			for block.started() == 0 {
				if time.Now().After(deadline) {
					t.Fatal("operation never started")
				}
				time.Sleep(time.Millisecond)
			}
			cancel()
			select {
			case <-callDone:
			case <-time.After(5 * time.Second):
				t.Fatal("Call did not return after the ctx was cancelled")
			}
			assertErrorCode(t, callErr, apperror.Cancelled)
			if block.disposedFor("gui-1") || len(inner.disposed) != 0 {
				t.Fatal("guest cancel must not Dispose the GUI SFTP handle")
			}
			if !block.interruptedFor("gui-1") {
				t.Fatal("guest cancel must Interrupt the in-flight client")
			}
			if block.started() != block.finished() {
				t.Fatalf("worker not joined: started %d finished %d", block.started(), block.finished())
			}
			cwd, err := inner.Cwd("gui-1")
			if err != nil || cwd != "/var/log" {
				t.Fatalf("cwd after guest cancel = %q (%v), want /var/log", cwd, err)
			}
		})
	}
}

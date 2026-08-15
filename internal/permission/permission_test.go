package permission

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestSensitive(t *testing.T) {
	want := map[string]bool{
		"bash": true, "sftp_write": true, "run_command": true,
		"sftp_upload": true, "sftp_download": true,
		"sftp_list": false, "sftp_read": false, "list_hosts": false,
		"list_sessions": false, "connect_host": false, "disconnect_session": false,
	}
	for tool, s := range want {
		if got := Sensitive(tool); got != s {
			t.Fatalf("Sensitive(%q) = %v, want %v", tool, got, s)
		}
	}
}

func TestParsePolicyAndDecision(t *testing.T) {
	if ParsePolicy("") != PolicyAsk || ParsePolicy("nope") != PolicyAsk {
		t.Fatal("unknown policy must fall back to ask")
	}
	if ParsePolicy("ALLOW") != PolicyAllow || ParsePolicy(" deny ") != PolicyDeny {
		t.Fatal("policy parse mismatch")
	}
	if _, ok := ParseDecision("allow-once"); ok {
		t.Fatal("unknown decision must be rejected")
	}
	if d, ok := ParseDecision("allow-session"); !ok || d != DecisionAllowSession {
		t.Fatalf("ParseDecision(allow-session) = %q %v", d, ok)
	}
}

func TestAuthorizeNilServiceAndNonSensitive(t *testing.T) {
	var s *Service
	if err := s.Authorize(context.Background(), Request{Tool: "bash"}); err != nil {
		t.Fatalf("nil service: %v", err)
	}
	svc := NewService(ServiceDeps{Gate: DenyGate{}})
	if err := svc.Authorize(context.Background(), Request{Tool: "sftp_read", SessionID: "s1"}); err != nil {
		t.Fatalf("read must not be gated: %v", err)
	}
}

func TestAuthorizePolicyAllowAndDenySkipGate(t *testing.T) {
	asks := 0
	gate := &countingGate{fn: func(Request) Decision {
		asks++
		return DecisionDeny
	}}
	allow := NewService(ServiceDeps{
		Gate:   gate,
		Policy: func() Policy { return PolicyAllow },
	})
	if err := allow.Authorize(context.Background(), Request{Tool: "bash", SessionID: "s1"}); err != nil {
		t.Fatalf("policy allow: %v", err)
	}
	deny := NewService(ServiceDeps{
		Gate:   gate,
		Policy: func() Policy { return PolicyDeny },
	})
	if err := deny.Authorize(context.Background(), Request{Tool: "bash", SessionID: "s1"}); !errors.Is(err, ErrDenied) {
		t.Fatalf("policy deny = %v, want ErrDenied", err)
	}
	if asks != 0 {
		t.Fatalf("gate asked %d times, want 0", asks)
	}
}

func TestAuthorizeNilGateAllowsUnderAsk(t *testing.T) {
	svc := NewService(ServiceDeps{Policy: func() Policy { return PolicyAsk }})
	if err := svc.Authorize(context.Background(), Request{Tool: "run_command", SessionID: "s1"}); err != nil {
		t.Fatalf("nil gate under ask must allow (tests): %v", err)
	}
}

func TestAuthorizeAskDenyAndAllowOnce(t *testing.T) {
	svc := NewService(ServiceDeps{Gate: DenyGate{}})
	if err := svc.Authorize(context.Background(), Request{Tool: "bash", SessionID: "s1"}); !errors.Is(err, ErrDenied) {
		t.Fatalf("deny gate = %v", err)
	}
	svc = NewService(ServiceDeps{Gate: AllowGate{}})
	if err := svc.Authorize(context.Background(), Request{Tool: "sftp_write", SessionID: "s1"}); err != nil {
		t.Fatalf("allow gate: %v", err)
	}
}

func TestAuthorizeAllowSessionRemembersPerTool(t *testing.T) {
	asks := 0
	gate := &countingGate{fn: func(Request) Decision {
		asks++
		return DecisionAllowSession
	}}
	svc := NewService(ServiceDeps{Gate: gate})
	req := Request{Tool: "bash", SessionID: "s1", Summary: "uptime"}
	if err := svc.Authorize(context.Background(), req); err != nil {
		t.Fatalf("first: %v", err)
	}
	if err := svc.Authorize(context.Background(), req); err != nil {
		t.Fatalf("second bash: %v", err)
	}
	if err := svc.Authorize(context.Background(), Request{Tool: "sftp_write", SessionID: "s1"}); err != nil {
		t.Fatalf("write: %v", err)
	}
	if asks != 2 {
		t.Fatalf("asks = %d, want 2 (bash grant must not cover write)", asks)
	}
	svc.ForgetSession("s1")
	if err := svc.Authorize(context.Background(), req); err != nil {
		t.Fatalf("after forget: %v", err)
	}
	if asks != 3 {
		t.Fatalf("asks after forget = %d, want 3", asks)
	}
}

func TestAuthorizeCancelledCtxDeniesWithoutAsking(t *testing.T) {
	asks := 0
	gate := &countingGate{fn: func(Request) Decision {
		asks++
		return DecisionAllowOnce
	}}
	svc := NewService(ServiceDeps{Gate: gate})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := svc.Authorize(ctx, Request{Tool: "bash", SessionID: "s1"}); !errors.Is(err, ErrDenied) {
		t.Fatalf("cancelled = %v, want ErrDenied", err)
	}
	if asks != 0 {
		t.Fatalf("cancelled ctx must not prompt, asks = %d", asks)
	}
}

func TestChannelGateDecideAndCancel(t *testing.T) {
	sink := &recSink{}
	g := NewChannelGate(sink)
	g.nextID = func() string { return "ask-1" }

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	got := make(chan Decision, 1)
	go func() {
		d, err := g.Ask(ctx, Request{Tool: "bash", SessionID: "s1", Summary: "uptime"})
		if err != nil {
			t.Errorf("Ask: %v", err)
		}
		got <- d
	}()
	deadline := time.Now().Add(time.Second)
	for sink.count(EventAsk) == 0 {
		if time.Now().After(deadline) {
			t.Fatal("timed out waiting for permission:ask")
		}
		time.Sleep(5 * time.Millisecond)
	}
	g.Decide("ask-1", DecisionAllowOnce)
	select {
	case d := <-got:
		if d != DecisionAllowOnce {
			t.Fatalf("decision = %q", d)
		}
	case <-ctx.Done():
		t.Fatal("Ask did not return after Decide")
	}

	g.nextID = func() string { return "ask-2" }
	ctx2, cancel2 := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel2()
	got2 := make(chan Decision, 1)
	go func() {
		d, _ := g.Ask(ctx2, Request{Tool: "bash", SessionID: "s9"})
		got2 <- d
	}()
	deadline = time.Now().Add(time.Second)
	for sink.count(EventAsk) < 2 {
		if time.Now().After(deadline) {
			t.Fatal("timed out waiting for second ask")
		}
		time.Sleep(5 * time.Millisecond)
	}
	g.CancelSession("s9")
	select {
	case d := <-got2:
		if d != DecisionDeny {
			t.Fatalf("cancelled session decision = %q", d)
		}
	case <-ctx2.Done():
		t.Fatal("Ask did not return after CancelSession")
	}
	if sink.count(EventClosed) != 1 {
		t.Fatalf("closed events = %d, want 1", sink.count(EventClosed))
	}
}

func TestChannelGateCtxCancelEmitsClosed(t *testing.T) {
	sink := &recSink{}
	g := NewChannelGate(sink)
	g.nextID = func() string { return "ask-c" }
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := g.Ask(ctx, Request{Tool: "bash", SessionID: "s1"})
		done <- err
	}()
	deadline := time.Now().Add(time.Second)
	for sink.count(EventAsk) == 0 {
		if time.Now().After(deadline) {
			t.Fatal("timed out waiting for ask")
		}
		time.Sleep(5 * time.Millisecond)
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Ask err = %v, want canceled", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Ask did not return after ctx cancel")
	}
	if sink.count(EventClosed) != 1 {
		t.Fatalf("closed events = %d, want 1", sink.count(EventClosed))
	}
}

func TestNativeGateInjectedPrompt(t *testing.T) {
	g := &NativeGate{Prompt: func(req Request) Decision {
		if req.Tool != "run_command" {
			t.Errorf("tool = %q", req.Tool)
		}
		return DecisionAllowOnce
	}}
	d, err := g.Ask(context.Background(), Request{Tool: "run_command", Summary: "id"})
	if err != nil || d != DecisionAllowOnce {
		t.Fatalf("Ask = %q %v", d, err)
	}
	g.Prompt = func(Request) Decision { return DecisionDeny }
	d, err = g.Ask(context.Background(), Request{Tool: "run_command"})
	if err != nil || d != DecisionDeny {
		t.Fatalf("deny Ask = %q %v", d, err)
	}
}

func TestNativeBodyOmitsSecretsAndBoundsLength(t *testing.T) {
	body := nativeBody(Request{
		Source:  SourceMCP,
		Tool:    "sftp_write",
		Title:   "prod-web",
		Summary: "/etc/passwd",
		Detail:  "12 bytes",
	})
	if strings.Contains(body, "root:") || strings.Contains(strings.ToLower(body), "password") {
		t.Fatalf("body leaked a secret: %q", body)
	}
	if !strings.Contains(body, "MCP") || !strings.Contains(body, "prod-web") || !strings.Contains(body, "/etc/passwd") {
		t.Fatalf("body missing context: %q", body)
	}
	huge := nativeBody(Request{Source: SourceAgent, Tool: "bash", Summary: strings.Repeat("a", 5000)})
	if len([]rune(huge)) > 2010 {
		t.Fatalf("native body too long: %d runes", len([]rune(huge)))
	}
}

func TestTruncate(t *testing.T) {
	if Truncate("  uptime  ") != "uptime" {
		t.Fatalf("trim = %q", Truncate("  uptime  "))
	}
	long := strings.Repeat("x", 200)
	got := Truncate(long)
	if !strings.HasSuffix(got, "…") || len([]rune(got)) != 161 {
		t.Fatalf("truncate = %q (%d runes)", got, len([]rune(got)))
	}
}

func TestErrDeniedCode(t *testing.T) {
	if ErrDenied.ErrorCode() != "PERMISSION_DENIED" {
		t.Fatalf("code = %q", ErrDenied.ErrorCode())
	}
}

type countingGate struct {
	fn func(Request) Decision
}

func (g *countingGate) Ask(_ context.Context, req Request) (Decision, error) {
	return g.fn(req), nil
}

type recSink struct {
	mu     sync.Mutex
	events []recEvent
}

type recEvent struct {
	name    string
	payload any
}

func (s *recSink) Emit(name string, payload any) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.events = append(s.events, recEvent{name: name, payload: payload})
}

func (s *recSink) count(name string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for _, e := range s.events {
		if e.name == name {
			n++
		}
	}
	return n
}

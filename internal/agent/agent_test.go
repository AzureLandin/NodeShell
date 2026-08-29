package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"nodeshell/internal/apperror"
	"nodeshell/internal/mcpcli"
	"nodeshell/internal/permission"
	"nodeshell/internal/sftpservice"
)

// The agent loop is exercised against a fake OpenAI-compatible endpoint and a
// fake session execer: no network, no SSH. What is asserted is the contract the
// UI and the remote host depend on — streamed deltas, tool dispatch bound to
// the caller's session, exactly one done event per accepted prompt, bounded
// transcripts, and no API key on any surface but the request header.

// --- fakes ---

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

func (s *recSink) snapshot() []recEvent {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]recEvent(nil), s.events...)
}

func (s *recSink) count(name string) int {
	n := 0
	for _, e := range s.snapshot() {
		if e.name == name {
			n++
		}
	}
	return n
}

// text concatenates every streamed delta, i.e. what the panel renders.
func (s *recSink) text() string {
	var b strings.Builder
	for _, e := range s.snapshot() {
		if e.name == EventDelta {
			b.WriteString(e.payload.(DeltaEvent).Delta)
		}
	}
	return b.String()
}

func (s *recSink) tools() []ToolEvent {
	var out []ToolEvent
	for _, e := range s.snapshot() {
		if e.name == EventTool {
			out = append(out, e.payload.(ToolEvent))
		}
	}
	return out
}

func (s *recSink) errors() []ErrorEvent {
	var out []ErrorEvent
	for _, e := range s.snapshot() {
		if e.name == EventError {
			out = append(out, e.payload.(ErrorEvent))
		}
	}
	return out
}

func (s *recSink) done() []DoneEvent {
	var out []DoneEvent
	for _, e := range s.snapshot() {
		if e.name == EventDone {
			out = append(out, e.payload.(DoneEvent))
		}
	}
	return out
}

type toolCallRec struct {
	sessionID string
	name      string
	command   string
	timeout   time.Duration
	args      map[string]any
}

type fakeTools struct {
	mu    sync.Mutex
	calls []toolCallRec
	out   string
	err   error
	// block, when set, holds a bash call until the context is cancelled.
	block bool
	// park, when non-nil, holds a bash call until the channel is closed.
	park <-chan struct{}

	entries   []sftpservice.Entry
	content   string
	written   string
	writePath string
	auth      permission.Authorizer
}

func (f *fakeTools) Call(ctx context.Context, sessionID, title, name string, args map[string]any) (any, error) {
	if err := f.authorize(ctx, sessionID, title, name, args); err != nil {
		return nil, err
	}

	command := stringFromArgs(args, "command")
	timeout := time.Duration(intFromArgs(args, "timeoutMs")) * time.Millisecond
	rec := toolCallRec{sessionID: sessionID, name: name, command: command, timeout: timeout, args: args}

	f.mu.Lock()
	f.calls = append(f.calls, rec)
	block, park, out, callErr := f.block, f.park, f.out, f.err
	entries, content := f.entries, f.content
	f.mu.Unlock()

	switch name {
	case toolBash:
		if park != nil {
			<-park
			return out, callErr
		}
		if block {
			<-ctx.Done()
			return nil, ctx.Err()
		}
		return out, callErr
	case toolSftpList:
		if callErr != nil {
			return nil, callErr
		}
		return mcpcli.SftpListResult{Entries: entries}, nil
	case toolSftpRead:
		if callErr != nil {
			return nil, callErr
		}
		return mcpcli.SftpReadResult{Path: stringFromArgs(args, "path"), Content: content}, nil
	case toolSftpWrite:
		if callErr != nil {
			return nil, callErr
		}
		path := stringFromArgs(args, "path")
		body := stringFromArgs(args, "content")
		f.mu.Lock()
		f.written, f.writePath = body, path
		f.mu.Unlock()
		return mcpcli.SftpWriteResult{OK: true, Path: path}, nil
	default:
		return nil, errf(apperror.Unknown, "unknown tool")
	}
}

func (f *fakeTools) authorize(ctx context.Context, sessionID, title, name string, args map[string]any) error {
	if f.auth == nil {
		return nil
	}
	summary := toolSummary(name, args)
	detail := ""
	if name == toolSftpWrite {
		detail = fmt.Sprintf("%d bytes", len(stringFromArgs(args, "content")))
	}
	return f.auth.Authorize(ctx, permission.Request{
		Source:    permission.SourceAgent,
		Tool:      name,
		SessionID: sessionID,
		Title:     title,
		Summary:   permission.Truncate(summary),
		Detail:    detail,
	})
}

func (f *fakeTools) recorded() []toolCallRec {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]toolCallRec(nil), f.calls...)
}

// --- fake endpoint ---

// scriptedCall is one tool call the fake model requests.
type scriptedCall struct {
	name string
	args string
}

// tool is the single-call shorthand for a scripted turn.
func tool(name, args string) []scriptedCall {
	return []scriptedCall{{name: name, args: args}}
}

// sseTurn is one scripted assistant turn: text fragments and any tool calls.
type sseTurn struct {
	deltas []string
	calls  []scriptedCall
}

// turnServer serves the scripted turns in order, one per request, and records
// every request body plus the Authorization header.
type turnServer struct {
	mu       sync.Mutex
	turns    []sseTurn
	requests []chatRequest
	auths    []string
	served   int
}

func (ts *turnServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	raw, _ := io.ReadAll(r.Body)
	var req chatRequest
	_ = json.Unmarshal(raw, &req)

	ts.mu.Lock()
	ts.requests = append(ts.requests, req)
	ts.auths = append(ts.auths, r.Header.Get("Authorization"))
	idx := ts.served
	ts.served++
	var turn sseTurn
	if idx < len(ts.turns) {
		turn = ts.turns[idx]
	}
	ts.mu.Unlock()

	w.Header().Set("Content-Type", "text/event-stream")
	flusher, _ := w.(http.Flusher)
	for _, d := range turn.deltas {
		fmt.Fprintf(w, "data: {\"choices\":[{\"delta\":{\"content\":%s}}]}\n\n", jsonString(d))
		if flusher != nil {
			flusher.Flush()
		}
	}
	for i, call := range turn.calls {
		// Split the arguments across two fragments: assembling them per index
		// is part of the protocol the loop must implement.
		half := len(call.args) / 2
		fmt.Fprintf(w, "data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":%d,\"id\":%s,\"type\":\"function\",\"function\":{\"name\":%s,\"arguments\":%s}}]}}]}\n\n",
			i, jsonString(fmt.Sprintf("call_%d", i+1)), jsonString(call.name), jsonString(call.args[:half]))
		fmt.Fprintf(w, "data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":%d,\"function\":{\"arguments\":%s}}]}}]}\n\n",
			i, jsonString(call.args[half:]))
	}
	fmt.Fprint(w, "data: [DONE]\n\n")
}

func (ts *turnServer) recorded() ([]chatRequest, []string) {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	return append([]chatRequest(nil), ts.requests...), append([]string(nil), ts.auths...)
}

func jsonString(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

// --- harness ---

type harness struct {
	svc    *Service
	sink   *recSink
	tools  *fakeTools
	server *turnServer
	url    string
}

func newHarness(t *testing.T, turns []sseTurn, tweak func(*Deps)) *harness {
	t.Helper()
	ts := &turnServer{turns: turns}
	srv := httptest.NewServer(ts)
	t.Cleanup(srv.Close)

	h := &harness{
		sink:   &recSink{},
		tools:  &fakeTools{out: "ok"},
		server: ts,
		url:    srv.URL,
	}
	deps := Deps{
		Tools:          h.tools,
		Sink:           h.sink,
		RequestTimeout: 5 * time.Second,
		ExecTimeout:    2 * time.Second,
	}
	if tweak != nil {
		tweak(&deps)
	}
	h.svc = New(deps)
	t.Cleanup(h.svc.DisposeAll)
	return h
}

func (h *harness) cfg() Config {
	return Config{BaseURL: h.url, Model: "test-model", APIKey: "sk-test-key"}
}

func (h *harness) prompt(sessionID, title, text string) error {
	return h.svc.Prompt(sessionID, title, text, h.cfg())
}

// waitDone blocks until n runs have closed; every accepted prompt must emit
// exactly one done event, so this is the run's completion signal.
func (h *harness) waitDone(t *testing.T, n int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		if h.sink.count(EventDone) >= n {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %d done events (got %d, errors: %v)",
				n, h.sink.count(EventDone), h.sink.errors())
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// --- tests ---

// A plain answer streams to the UI as deltas and closes with one done event.
func TestPromptStreamsTextAndClosesOnce(t *testing.T) {
	h := newHarness(t, []sseTurn{{deltas: []string{"Disk ", "looks ", "fine."}}}, nil)

	if err := h.prompt("s1", "prod-web", "how is the disk?"); err != nil {
		t.Fatalf("Prompt: %v", err)
	}
	h.waitDone(t, 1)

	if got := h.sink.text(); got != "Disk looks fine." {
		t.Fatalf("streamed text = %q", got)
	}
	if got := h.sink.count(EventDone); got != 1 {
		t.Fatalf("done events = %d, want exactly 1", got)
	}
	if errs := h.sink.errors(); len(errs) != 0 {
		t.Fatalf("unexpected error events: %v", errs)
	}
	if done := h.sink.done(); done[0].Aborted {
		t.Fatal("a completed run must not report aborted")
	}
}

// The tool call runs against the session the prompt named — never one the
// model could pick — and the loop feeds the result back for a second turn.
func TestToolCallRunsOnPromptSessionAndLoopsBack(t *testing.T) {
	h := newHarness(t, []sseTurn{
		{deltas: []string{"checking"}, calls: tool(toolBash, `{"command":"df -h"}`)},
		{deltas: []string{"all good"}},
	}, nil)
	h.tools.out = "/dev/sda1 40% /"

	if err := h.prompt("s7", "prod-web", "check disk"); err != nil {
		t.Fatalf("Prompt: %v", err)
	}
	h.waitDone(t, 1)

	calls := h.tools.recorded()
	if len(calls) != 1 {
		t.Fatalf("exec calls = %d, want 1", len(calls))
	}
	if calls[0].sessionID != "s7" {
		t.Fatalf("exec session = %q, want the prompt's session s7", calls[0].sessionID)
	}
	if calls[0].command != "df -h" {
		t.Fatalf("exec command = %q", calls[0].command)
	}
	tools := h.sink.tools()
	if len(tools) != 1 || tools[0].Name != toolBash || !tools[0].OK || tools[0].Summary != "df -h" {
		t.Fatalf("tool events = %+v", tools)
	}
	if got := h.sink.text(); got != "checkingall good" {
		t.Fatalf("text across turns = %q", got)
	}

	// The follow-up request must carry the assistant's tool call and the tool
	// result, otherwise the endpoint rejects the transcript.
	reqs, _ := h.server.recorded()
	if len(reqs) != 2 {
		t.Fatalf("requests = %d, want 2 turns", len(reqs))
	}
	last := reqs[1].Messages
	var sawCall, sawResult bool
	for _, m := range last {
		if m.Role == roleAssistant && len(m.ToolCalls) == 1 {
			sawCall = true
		}
		if m.Role == roleTool && m.ToolCallID == "call_1" && strings.Contains(m.Content, "/dev/sda1") {
			sawResult = true
		}
	}
	if !sawCall || !sawResult {
		t.Fatalf("second request missing tool pairing: %+v", last)
	}
}

// A sessionId the model stuffed into the tool arguments is ignored: the loop
// always addresses the session Prompt named.
func TestToolCallIgnoresSessionIdInArguments(t *testing.T) {
	h := newHarness(t, []sseTurn{
		{calls: tool(toolBash, `{"command":"id","sessionId":"other-host"}`)},
		{deltas: []string{"ok"}},
	}, nil)

	if err := h.prompt("s7", "prod-web", "whoami"); err != nil {
		t.Fatalf("Prompt: %v", err)
	}
	h.waitDone(t, 1)

	calls := h.tools.recorded()
	if len(calls) != 1 || calls[0].sessionID != "s7" {
		t.Fatalf("tool session = %+v, want the prompt's s7", calls)
	}
}

// A model-supplied timeout is clamped to the service bound, so the model can
// shorten a command but never outlast the configured limit.
func TestBashTimeoutIsClamped(t *testing.T) {
	h := newHarness(t, []sseTurn{
		{calls: tool(toolBash, `{"command":"sleep 1","timeoutMs":600000}`)},
		{deltas: []string{"done"}},
	}, nil)

	if err := h.prompt("s1", "host", "run it"); err != nil {
		t.Fatalf("Prompt: %v", err)
	}
	h.waitDone(t, 1)

	calls := h.tools.recorded()
	if len(calls) != 1 || calls[0].timeout != 2*time.Second {
		t.Fatalf("exec timeout = %v, want the 2s service bound", calls)
	}
}

// A failing tool is reported to the UI and handed back to the model as text:
// the run continues instead of dying on a bad command.
func TestFailedToolIsReportedAndFedBack(t *testing.T) {
	h := newHarness(t, []sseTurn{
		{calls: tool(toolBash, `{"command":"cat /root/x"}`)},
		{deltas: []string{"permission denied"}},
	}, nil)
	h.tools.err = &Error{Code: apperror.Unknown, Message: "exit status 1"}

	if err := h.prompt("s1", "host", "read it"); err != nil {
		t.Fatalf("Prompt: %v", err)
	}
	h.waitDone(t, 1)

	tools := h.sink.tools()
	if len(tools) != 1 || tools[0].OK {
		t.Fatalf("tool event should be marked failed: %+v", tools)
	}
	if !strings.Contains(tools[0].Detail, "exit status 1") {
		t.Fatalf("tool detail = %q, want the failure text", tools[0].Detail)
	}
	if errs := h.sink.errors(); len(errs) != 0 {
		t.Fatalf("a failed tool must not fail the run: %v", errs)
	}
	reqs, _ := h.server.recorded()
	var fed bool
	for _, m := range reqs[1].Messages {
		if m.Role == roleTool && strings.HasPrefix(m.Content, "error:") {
			fed = true
		}
	}
	if !fed {
		t.Fatal("the tool failure was not fed back to the model")
	}
}

// A denied sensitive tool must not touch the remote host; the denial is a
// failed tool result so the model can stop, not a run-level error.
func TestDeniedSensitiveToolDoesNotExec(t *testing.T) {
	auth := permission.NewService(permission.ServiceDeps{Gate: permission.DenyGate{}})
	h := newHarness(t, []sseTurn{
		{calls: tool(toolBash, `{"command":"rm -rf /"}`)},
		{deltas: []string{"blocked"}},
	}, nil)
	h.tools.auth = auth

	if err := h.prompt("s1", "prod-web", "wipe it"); err != nil {
		t.Fatalf("Prompt: %v", err)
	}
	h.waitDone(t, 1)

	if calls := h.tools.recorded(); len(calls) != 0 {
		t.Fatalf("exec ran despite deny: %+v", calls)
	}
	tools := h.sink.tools()
	if len(tools) != 1 || tools[0].OK || tools[0].Summary != "rm -rf /" {
		t.Fatalf("tool event = %+v", tools)
	}
	if !strings.Contains(tools[0].Detail, "Permission denied") {
		t.Fatalf("tool detail = %q", tools[0].Detail)
	}
}

// Reads are not gated: a deny-all Gate must still let sftp_read through.
func TestReadToolSkipsPermissionGate(t *testing.T) {
	auth := permission.NewService(permission.ServiceDeps{Gate: permission.DenyGate{}})
	h := newHarness(t, []sseTurn{
		{calls: tool(toolSftpRead, `{"path":"/etc/hosts"}`)},
		{deltas: []string{"ok"}},
	}, nil)
	h.tools.auth = auth
	h.tools.content = "127.0.0.1 localhost"

	if err := h.prompt("s1", "prod-web", "read hosts"); err != nil {
		t.Fatalf("Prompt: %v", err)
	}
	h.waitDone(t, 1)

	if len(h.tools.recorded()) != 1 {
		t.Fatalf("read was not issued: %+v", h.tools.recorded())
	}
	tools := h.sink.tools()
	if len(tools) != 1 || !tools[0].OK {
		t.Fatalf("read should succeed under a deny gate: %+v", tools)
	}
}

// A denied write must not call WriteText, and the request must not carry the
// file contents — only the path and a byte count.
func TestDeniedWriteDoesNotWrite(t *testing.T) {
	var seen []permission.Request
	auth := permission.NewService(permission.ServiceDeps{
		Gate: &recordingGate{fn: func(req permission.Request) permission.Decision {
			seen = append(seen, req)
			return permission.DecisionDeny
		}},
	})
	h := newHarness(t, []sseTurn{
		{calls: tool(toolSftpWrite, `{"path":"/tmp/x","content":"SECRET"}`)},
		{deltas: []string{"no"}},
	}, nil)
	h.tools.auth = auth

	if err := h.prompt("s1", "prod-web", "write it"); err != nil {
		t.Fatalf("Prompt: %v", err)
	}
	h.waitDone(t, 1)

	if h.tools.writePath != "" || h.tools.written != "" {
		t.Fatalf("write ran despite deny: path=%q written=%q", h.tools.writePath, h.tools.written)
	}
	if len(seen) != 1 || seen[0].Summary != "/tmp/x" || seen[0].Detail != "6 bytes" {
		t.Fatalf("permission request = %+v", seen)
	}
	if strings.Contains(seen[0].Summary, "SECRET") || strings.Contains(seen[0].Detail, "SECRET") {
		t.Fatalf("write contents leaked into the prompt: %+v", seen[0])
	}
}

type recordingGate struct {
	fn func(permission.Request) permission.Decision
}

func (g *recordingGate) Ask(_ context.Context, req permission.Request) (permission.Decision, error) {
	return g.fn(req), nil
}

// The file tools advertise the MCP 512KiB cap; read/write still round-trip
// through the tool caller so a large remote file is the runtime's problem.
func TestFileToolsUseMCPByteCap(t *testing.T) {
	if MaxFileBytes != mcpcli.MaxFileBytes {
		t.Fatalf("agent file cap = %d, want MCP MaxFileBytes (%d)", MaxFileBytes, mcpcli.MaxFileBytes)
	}
	h := newHarness(t, []sseTurn{
		{calls: tool(toolSftpRead, `{"path":"/etc/hosts"}`)},
		{calls: tool(toolSftpWrite, `{"path":"/tmp/out","content":"hello"}`)},
		{deltas: []string{"written"}},
	}, nil)
	h.tools.content = "127.0.0.1 localhost"

	if err := h.prompt("s1", "host", "sync hosts"); err != nil {
		t.Fatalf("Prompt: %v", err)
	}
	h.waitDone(t, 1)

	h.tools.mu.Lock()
	written, writePath := h.tools.written, h.tools.writePath
	h.tools.mu.Unlock()
	if written != "hello" || writePath != "/tmp/out" {
		t.Fatalf("write = (%q, %q)", writePath, written)
	}
	tools := h.sink.tools()
	if len(tools) != 2 || !tools[0].OK || !tools[1].OK {
		t.Fatalf("tool events = %+v", tools)
	}
}

// Abort stops a blocked tool and closes the run as aborted, not as a failure:
// the user asked for the stop.
func TestAbortStopsRunAndReportsAborted(t *testing.T) {
	h := newHarness(t, []sseTurn{
		{calls: tool(toolBash, `{"command":"tail -f /var/log/syslog"}`)},
	}, nil)
	h.tools.block = true

	if err := h.prompt("s1", "host", "tail the log"); err != nil {
		t.Fatalf("Prompt: %v", err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for len(h.tools.recorded()) == 0 {
		if time.Now().After(deadline) {
			t.Fatal("timed out waiting for the tool to start")
		}
		time.Sleep(5 * time.Millisecond)
	}
	h.svc.Abort("s1")
	h.waitDone(t, 1)

	done := h.sink.done()
	if len(done) != 1 || !done[0].Aborted {
		t.Fatalf("done events = %+v, want one aborted", done)
	}
	if errs := h.sink.errors(); len(errs) != 0 {
		t.Fatalf("an abort must not surface as an error: %v", errs)
	}
}

// Aborting a turn that requested several tools leaves the later calls
// unanswered: the loop stops before running them. The transcript must still be
// one the endpoint accepts, because an assistant tool_calls message without a
// result for every call is rejected — the abort would otherwise break the whole
// conversation instead of just ending the run.
func TestAbortMidToolLeavesUsableTranscript(t *testing.T) {
	h := newHarness(t, []sseTurn{
		{calls: []scriptedCall{
			{name: toolBash, args: `{"command":"tail -f log"}`},
			{name: toolBash, args: `{"command":"uptime"}`},
		}},
		{deltas: []string{"back again"}},
	}, nil)
	h.tools.block = true

	if err := h.prompt("s1", "host", "tail it"); err != nil {
		t.Fatalf("Prompt: %v", err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for len(h.tools.recorded()) == 0 {
		if time.Now().After(deadline) {
			t.Fatal("timed out waiting for the tool to start")
		}
		time.Sleep(5 * time.Millisecond)
	}
	h.svc.Abort("s1")
	h.waitDone(t, 1)

	h.tools.mu.Lock()
	h.tools.block = false
	h.tools.mu.Unlock()
	if err := h.prompt("s1", "host", "never mind, what is up?"); err != nil {
		t.Fatalf("Prompt after abort: %v", err)
	}
	h.waitDone(t, 2)

	if errs := h.sink.errors(); len(errs) != 0 {
		t.Fatalf("the follow-up run failed: %v", errs)
	}
	reqs, _ := h.server.recorded()
	msgs := reqs[len(reqs)-1].Messages
	answered := map[string]bool{}
	var pending []string
	for _, m := range msgs {
		if m.Role == roleTool {
			answered[m.ToolCallID] = true
		}
		for _, call := range m.ToolCalls {
			pending = append(pending, call.ID)
		}
	}
	for _, id := range pending {
		if !answered[id] {
			t.Fatalf("tool call %q has no result in the replayed transcript: %+v", id, msgs)
		}
	}
}

// A second prompt while a run is in flight is rejected up front, so one
// session can never have two loops appending to the same transcript.
func TestConcurrentPromptRejected(t *testing.T) {
	h := newHarness(t, []sseTurn{
		{calls: tool(toolBash, `{"command":"sleep"}`)},
	}, nil)
	h.tools.block = true

	if err := h.prompt("s1", "host", "first"); err != nil {
		t.Fatalf("Prompt: %v", err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for len(h.tools.recorded()) == 0 {
		if time.Now().After(deadline) {
			t.Fatal("timed out waiting for the first run")
		}
		time.Sleep(5 * time.Millisecond)
	}
	if err := h.prompt("s1", "host", "second"); err == nil {
		t.Fatal("a second prompt during a run must be rejected")
	}
	h.svc.Abort("s1")
	h.waitDone(t, 1)
}

// Dispose (session:closed) cancels the run and drops the transcript, so a
// reconnect under the same id never inherits the old conversation.
func TestDisposeCancelsRunAndDropsHistory(t *testing.T) {
	h := newHarness(t, []sseTurn{
		{calls: tool(toolBash, `{"command":"sleep"}`)},
		{deltas: []string{"fresh"}},
	}, nil)
	h.tools.block = true

	if err := h.prompt("s1", "host", "remember this"); err != nil {
		t.Fatalf("Prompt: %v", err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for len(h.tools.recorded()) == 0 {
		if time.Now().After(deadline) {
			t.Fatal("timed out waiting for the run to start")
		}
		time.Sleep(5 * time.Millisecond)
	}
	h.svc.Dispose("s1")
	if got := h.sink.count(EventDone); got != 0 {
		t.Fatalf("disposed run must not emit done (got %d)", got)
	}

	h.tools.mu.Lock()
	h.tools.block = false
	h.tools.mu.Unlock()
	if err := h.prompt("s1", "host", "new question"); err != nil {
		t.Fatalf("Prompt after dispose: %v", err)
	}
	h.waitDone(t, 1)

	reqs, _ := h.server.recorded()
	last := reqs[len(reqs)-1].Messages
	for _, m := range last {
		if strings.Contains(m.Content, "remember this") {
			t.Fatalf("disposed transcript leaked into a later run: %+v", last)
		}
	}
}

// Clear detaches the in-flight conversation so the next prompt cannot replay
// the emptied history, even if the cancelled run later tries to append.
func TestClearDuringRunDropsHistory(t *testing.T) {
	h := newHarness(t, []sseTurn{
		{calls: tool(toolBash, `{"command":"sleep"}`)},
		{deltas: []string{"fresh"}},
	}, nil)
	h.tools.block = true

	if err := h.prompt("s1", "host", "remember this"); err != nil {
		t.Fatalf("Prompt: %v", err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for len(h.tools.recorded()) == 0 {
		if time.Now().After(deadline) {
			t.Fatal("timed out waiting for the run to start")
		}
		time.Sleep(5 * time.Millisecond)
	}
	h.svc.Clear("s1")

	h.tools.mu.Lock()
	h.tools.block = false
	h.tools.mu.Unlock()
	if err := h.prompt("s1", "host", "new question"); err != nil {
		t.Fatalf("Prompt after clear must not wait on the detached run: %v", err)
	}
	h.waitDone(t, 1)

	reqs, _ := h.server.recorded()
	last := reqs[len(reqs)-1].Messages
	for _, m := range last {
		if strings.Contains(m.Content, "remember this") {
			t.Fatalf("cleared transcript leaked into a later run: %+v", last)
		}
	}
	if h.sink.count(EventDone) != 1 {
		t.Fatalf("detached run must not emit done; only the new run does (got %d)", h.sink.count(EventDone))
	}
}

// DisposeAll must not return while a tool call is still executing, so shutdown
// cannot tear down SSH under a live bash/sftp_write.
func TestDisposeAllJoinsInFlightRun(t *testing.T) {
	park := make(chan struct{})
	h := newHarness(t, []sseTurn{
		{calls: tool(toolBash, `{"command":"sleep"}`)},
	}, nil)
	h.tools.park = park
	t.Cleanup(func() {
		select {
		case <-park:
		default:
			close(park)
		}
	})

	if err := h.prompt("s1", "host", "hold"); err != nil {
		t.Fatalf("Prompt: %v", err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for len(h.tools.recorded()) == 0 {
		if time.Now().After(deadline) {
			t.Fatal("timed out waiting for the run to start")
		}
		time.Sleep(5 * time.Millisecond)
	}

	finished := make(chan struct{})
	go func() {
		h.svc.DisposeAll()
		close(finished)
	}()
	select {
	case <-finished:
		t.Fatal("DisposeAll returned before the in-flight tool finished")
	case <-time.After(50 * time.Millisecond):
	}
	close(park)
	select {
	case <-finished:
	case <-time.After(5 * time.Second):
		t.Fatal("DisposeAll did not return after the tool finished")
	}
}

type hookAuthorizer struct {
	onAuth func()
}

func (h *hookAuthorizer) Authorize(ctx context.Context, req permission.Request) error {
	if h.onAuth != nil {
		h.onAuth()
	}
	return nil
}

// A default run (MaxTurns = 0) can execute more than 8 sequential tool turns
// without hitting any step-limit error and finishes successfully when the model
// returns a final answer.
func TestDefaultRunCanExceedEightToolTurnsAndFinish(t *testing.T) {
	const totalToolTurns = 12
	turns := make([]sseTurn, totalToolTurns+1)
	for i := 0; i < totalToolTurns; i++ {
		turns[i] = sseTurn{calls: tool(toolBash, fmt.Sprintf(`{"command":"echo step %d"}`, i+1))}
	}
	turns[totalToolTurns] = sseTurn{deltas: []string{"all 12 steps completed successfully"}}

	h := newHarness(t, turns, nil) // Deps.MaxTurns defaults to 0 (unlimited)
	if err := h.prompt("s1", "host", "run 12-step task"); err != nil {
		t.Fatalf("Prompt: %v", err)
	}
	h.waitDone(t, 1)

	calls := h.tools.recorded()
	if len(calls) != totalToolTurns {
		t.Fatalf("exec calls = %d, want %d", len(calls), totalToolTurns)
	}
	if got := h.sink.text(); got != "all 12 steps completed successfully" {
		t.Fatalf("streamed text = %q, want final answer", got)
	}
	if errs := h.sink.errors(); len(errs) != 0 {
		t.Fatalf("unexpected error events: %+v", errs)
	}
	done := h.sink.done()
	if len(done) != 1 {
		t.Fatalf("done count = %d, want 1", len(done))
	}
	if done[0].Aborted {
		t.Fatal("successful run must not report aborted")
	}
}

// An unlimited run executing beyond 8 turns can be aborted cleanly at any turn.
func TestUnlimitedRunCanAbortAfterEightTurns(t *testing.T) {
	const totalTurns = 15
	turns := make([]sseTurn, totalTurns)
	for i := range turns {
		turns[i] = sseTurn{calls: tool(toolBash, fmt.Sprintf(`{"command":"echo step %d"}`, i+1))}
	}

	var h *harness
	var once sync.Once
	h = newHarness(t, turns, nil)
	h.tools.auth = &hookAuthorizer{
		onAuth: func() {
			if len(h.tools.recorded()) == 10 {
				once.Do(func() {
					h.svc.Abort("s1")
				})
			}
		},
	}

	if err := h.prompt("s1", "host", "run forever"); err != nil {
		t.Fatalf("Prompt: %v", err)
	}
	h.waitDone(t, 1)

	calls := h.tools.recorded()
	if len(calls) < 10 {
		t.Fatalf("exec calls = %d, want at least 10 before abort", len(calls))
	}
	if errs := h.sink.errors(); len(errs) != 0 {
		t.Fatalf("unexpected error events: %+v", errs)
	}
	done := h.sink.done()
	if len(done) != 1 {
		t.Fatalf("done count = %d, want 1", len(done))
	}
	if !done[0].Aborted {
		t.Fatal("aborted run must report aborted = true")
	}
}

// An explicit turn limit (MaxTurns > 0) is observable: a model that keeps
// calling tools ends with an error event rather than spinning indefinitely.
func TestExplicitTurnLimitEndsWithError(t *testing.T) {
	turns := make([]sseTurn, 6)
	for i := range turns {
		turns[i] = sseTurn{calls: tool(toolBash, `{"command":"echo loop"}`)}
	}
	h := newHarness(t, turns, func(d *Deps) { d.MaxTurns = 3 })

	if err := h.prompt("s1", "host", "loop"); err != nil {
		t.Fatalf("Prompt: %v", err)
	}
	h.waitDone(t, 1)

	if got := len(h.tools.recorded()); got != 3 {
		t.Fatalf("exec calls = %d, want the 3-turn cap", got)
	}
	errs := h.sink.errors()
	if len(errs) != 1 || !strings.Contains(errs[0].Error.Message, "3 steps") {
		t.Fatalf("error events = %+v, want one step-limit error", errs)
	}
}

// The API key goes into the Authorization header and nowhere else: not into
// the request body, and not into any event the WebView can read.
func TestAPIKeyOnlyTravelsInAuthorizationHeader(t *testing.T) {
	h := newHarness(t, []sseTurn{{deltas: []string{"hi"}}}, nil)

	if err := h.prompt("s1", "host", "hello"); err != nil {
		t.Fatalf("Prompt: %v", err)
	}
	h.waitDone(t, 1)

	reqs, auths := h.server.recorded()
	if len(auths) != 1 || auths[0] != "Bearer sk-test-key" {
		t.Fatalf("authorization headers = %v", auths)
	}
	body, _ := json.Marshal(reqs)
	if strings.Contains(string(body), "sk-test-key") {
		t.Fatal("the API key must not appear in the request body")
	}
	events, _ := json.Marshal(h.sink.snapshot())
	if strings.Contains(string(events), "sk-test-key") {
		t.Fatal("the API key must not appear in any emitted event")
	}
}

// An endpoint that echoes the key in an error must not put it on screen.
func TestProviderErrorRedactsKeyAndCodesTheFailure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		fmt.Fprint(w, `{"error":{"message":"Incorrect API key provided: sk-test-key"}}`)
	}))
	defer srv.Close()

	sink := &recSink{}
	svc := New(Deps{
		Tools: &fakeTools{},
		Sink:  sink,
	})
	defer svc.DisposeAll()

	if err := svc.Prompt("s1", "host", "hello", Config{BaseURL: srv.URL, Model: "m", APIKey: "sk-test-key"}); err != nil {
		t.Fatalf("Prompt: %v", err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for sink.count(EventDone) == 0 {
		if time.Now().After(deadline) {
			t.Fatal("timed out waiting for the failed run to close")
		}
		time.Sleep(5 * time.Millisecond)
	}
	errs := sink.errors()
	if len(errs) != 1 {
		t.Fatalf("error events = %+v, want one", errs)
	}
	if strings.Contains(errs[0].Error.Message, "sk-test-key") {
		t.Fatalf("the key leaked into the error: %q", errs[0].Error.Message)
	}
	if !strings.Contains(errs[0].Error.Message, "[redacted]") ||
		!strings.Contains(errs[0].Error.Message, "401") {
		t.Fatalf("error message = %q, want the redacted 401 detail", errs[0].Error.Message)
	}
}

// An unconfigured agent rejects before any request is built, so the UI can
// point at settings instead of showing a failed run.
func TestPromptRejectedWhenUnconfigured(t *testing.T) {
	sink := &recSink{}
	svc := New(Deps{Sink: sink})
	defer svc.DisposeAll()

	if err := svc.Prompt("s1", "host", "hello", Config{BaseURL: "https://x.test/v1", Model: "m"}); err == nil {
		t.Fatal("a missing API key must reject the prompt")
	}
	if got := len(sink.snapshot()); got != 0 {
		t.Fatalf("a rejected prompt must emit nothing, got %d events", got)
	}
	cfg := Config{BaseURL: "https://x.test/v1", Model: "m", APIKey: "k"}
	if err := svc.Prompt("", "host", "hello", cfg); err == nil {
		t.Fatal("a prompt without a session must be rejected")
	}
	if err := svc.Prompt("s1", "host", "   ", cfg); err == nil {
		t.Fatal("a blank prompt must be rejected")
	}
	if err := svc.Prompt("s1", "host", strings.Repeat("x", MaxPromptBytes+1), cfg); err == nil {
		t.Fatal("an oversized prompt must be rejected")
	}
}

// After shutdown no run may start: the WebView is gone, so its events could
// never be observed.
func TestPromptRejectedAfterDisposeAll(t *testing.T) {
	h := newHarness(t, []sseTurn{{deltas: []string{"hi"}}}, nil)
	h.svc.DisposeAll()
	if err := h.prompt("s1", "host", "hello"); err == nil {
		t.Fatal("Prompt after DisposeAll must be rejected")
	}
}

// A tool result larger than the transcript cap is truncated with a marker, so
// one noisy command cannot grow the next request without bound.
func TestOversizedToolResultTruncated(t *testing.T) {
	h := newHarness(t, []sseTurn{
		{calls: tool(toolBash, `{"command":"cat big"}`)},
		{deltas: []string{"ok"}},
	}, nil)
	h.tools.out = strings.Repeat("y", MaxToolResultBytes*2)

	if err := h.prompt("s1", "host", "dump it"); err != nil {
		t.Fatalf("Prompt: %v", err)
	}
	h.waitDone(t, 1)

	reqs, _ := h.server.recorded()
	for _, m := range reqs[1].Messages {
		if m.Role != roleTool {
			continue
		}
		if len(m.Content) > MaxToolResultBytes+64 {
			t.Fatalf("tool result not truncated: %d bytes", len(m.Content))
		}
		if !strings.Contains(m.Content, "truncated") {
			t.Fatal("truncation must be marked so the model knows it saw a prefix")
		}
		return
	}
	t.Fatal("no tool result in the follow-up request")
}

// The transcript cap must never leave a leading tool result: an orphan tool
// message is rejected by the API.
func TestTrimHistoryNeverLeavesOrphanToolResult(t *testing.T) {
	msgs := make([]chatMessage, 0, MaxHistoryMessages+4)
	msgs = append(msgs, chatMessage{Role: roleUser, Content: "first"})
	for i := 0; i < MaxHistoryMessages+2; i++ {
		msgs = append(msgs,
			chatMessage{Role: roleAssistant, ToolCalls: []toolCall{{ID: fmt.Sprint(i)}}},
			chatMessage{Role: roleTool, ToolCallID: fmt.Sprint(i), Content: "out"})
	}
	got := trimHistory(msgs)
	if len(got) > MaxHistoryMessages {
		t.Fatalf("history = %d messages, want <= %d", len(got), MaxHistoryMessages)
	}
	if got[0].Role == roleTool {
		t.Fatal("trimmed history starts with an orphan tool result")
	}
}

// The system prompt pins the session's host and states the no-local-machine
// boundary; the advertised tools are exactly the remote four.
func TestSystemPromptAndToolSurface(t *testing.T) {
	h := newHarness(t, []sseTurn{{deltas: []string{"hi"}}}, nil)
	if err := h.prompt("s1", "prod-web", "hello"); err != nil {
		t.Fatalf("Prompt: %v", err)
	}
	h.waitDone(t, 1)

	reqs, _ := h.server.recorded()
	system := reqs[0].Messages[0]
	if system.Role != roleSystem {
		t.Fatalf("first message role = %q, want system", system.Role)
	}
	if !strings.Contains(system.Content, "prod-web") {
		t.Fatalf("system prompt does not name the host: %q", system.Content)
	}
	if !strings.Contains(system.Content, "cannot see the user's local machine") {
		t.Fatalf("system prompt drops the local-machine boundary: %q", system.Content)
	}
	want := map[string]bool{toolBash: true, toolSftpList: true, toolSftpRead: true, toolSftpWrite: true}
	if len(reqs[0].Tools) != len(want) {
		t.Fatalf("advertised %d tools, want %d", len(reqs[0].Tools), len(want))
	}
	for _, spec := range reqs[0].Tools {
		if !want[spec.Function.Name] {
			t.Fatalf("unexpected tool advertised: %q", spec.Function.Name)
		}
		if _, ok := spec.Function.Parameters["properties"].(map[string]any)["sessionId"]; ok {
			t.Fatalf("%s exposes a sessionId argument; the session must be injected",
				spec.Function.Name)
		}
	}
}

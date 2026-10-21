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

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"

	"nodeshell/internal/apperror"
	"nodeshell/internal/mcpcli"
	"nodeshell/internal/permission"
	"nodeshell/internal/sftpservice"
)

// The agent is exercised against a scripted model and a fake session execer:
// no network, no SSH. What is asserted is the contract the UI and the remote
// host depend on — streamed deltas, tool dispatch bound to the caller's
// session, exactly one done event per accepted prompt, bounded transcripts,
// and no API key on any surface but the request header.

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

// --- scripted model ---

// fakeTurn is one scripted assistant turn: the text frames it streams, the
// reasoning it hides, and the tools it asks for.
type fakeTurn struct {
	chunks    []string
	reasoning string
	calls     []schema.ToolCall
	// err fails the model call itself, the way a provider rejects a request.
	err error
	// block holds the stream open after the frames until the run is
	// cancelled, so a test can abort in the middle of an answer.
	block bool
}

// toolCall is the shorthand for one requested tool call.
func toolCallFor(id, name, args string) schema.ToolCall {
	return schema.ToolCall{
		ID:       id,
		Type:     "function",
		Function: schema.FunctionCall{Name: name, Arguments: args},
	}
}

// fakeModel is a scripted model.ToolCallingChatModel. It replays its turns
// one per request and records what it was asked, so a test can assert what
// the transcript looked like when the model was called.
type fakeModel struct {
	mu    sync.Mutex
	turns []fakeTurn
	calls int
	// requests holds the message list of every call, in order.
	requests [][]*schema.Message
	// tools holds the tool schemas the agent advertised.
	tools []*schema.ToolInfo
}

func (f *fakeModel) Generate(ctx context.Context, in []*schema.Message, opts ...model.Option) (*schema.Message, error) {
	turn, err := f.next(in, opts)
	if err != nil {
		return nil, err
	}
	return concatChunks(turn.frames()), nil
}

func (f *fakeModel) Stream(ctx context.Context, in []*schema.Message, opts ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	turn, err := f.next(in, opts)
	if err != nil {
		return nil, err
	}
	frames := turn.frames()
	if !turn.block {
		return schema.StreamReaderFromArray(frames), nil
	}
	sr, sw := schema.Pipe[*schema.Message](1)
	go func() {
		defer sw.Close()
		for _, frame := range frames {
			if sw.Send(frame, nil) {
				return
			}
		}
		// The answer never finishes: only the cancellation ends the stream.
		<-ctx.Done()
		sw.Send(nil, ctx.Err())
	}()
	return sr, nil
}

func (f *fakeModel) WithTools(tools []*schema.ToolInfo) (model.ToolCallingChatModel, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.tools = tools
	return f, nil
}

func (f *fakeModel) GetType() string { return "fake" }

func (f *fakeModel) IsCallbacksEnabled() bool { return false }

// next records the request and returns the scripted turn for it.
func (f *fakeModel) next(in []*schema.Message, opts []model.Option) (fakeTurn, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.requests = append(f.requests, in)
	if common := model.GetCommonOptions(&model.Options{}, opts...); len(common.Tools) > 0 {
		f.tools = common.Tools
	}
	if f.calls >= len(f.turns) {
		return fakeTurn{}, fmt.Errorf("fake model: no scripted turn %d", f.calls)
	}
	turn := f.turns[f.calls]
	f.calls++
	return turn, turn.err
}

// frames turns a scripted turn into the message frames the model streams.
func (t fakeTurn) frames() []*schema.Message {
	frames := make([]*schema.Message, 0, len(t.chunks)+2)
	for _, chunk := range t.chunks {
		frames = append(frames, &schema.Message{Role: schema.Assistant, Content: chunk})
	}
	if t.reasoning != "" {
		frames = append(frames, &schema.Message{Role: schema.Assistant, ReasoningContent: t.reasoning})
	}
	if len(t.calls) > 0 {
		frames = append(frames, &schema.Message{Role: schema.Assistant, ToolCalls: t.calls})
	}
	return frames
}

// request returns the messages of the n-th model call.
func (f *fakeModel) request(n int) []*schema.Message {
	f.mu.Lock()
	defer f.mu.Unlock()
	if n >= len(f.requests) {
		return nil
	}
	return f.requests[n]
}

// callCount is the number of model calls made so far.
func (f *fakeModel) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

// advertisedTools returns the tool schemas the agent offered to the model.
func (f *fakeModel) advertisedTools() []*schema.ToolInfo {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.tools
}

// --- fake tools ---

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

// --- harness ---

type harness struct {
	svc   *Service
	sink  *recSink
	tools *fakeTools
	model *fakeModel
}

func newHarness(t *testing.T, turns []fakeTurn, tweak func(*Deps)) *harness {
	t.Helper()
	h := &harness{
		sink:  &recSink{},
		tools: &fakeTools{out: "ok"},
		model: &fakeModel{turns: turns},
	}
	deps := Deps{
		Tools: h.tools,
		Sink:  h.sink,
		Model: func(context.Context, Config) (model.ToolCallingChatModel, error) {
			return h.model, nil
		},
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
	return Config{BaseURL: "https://model.test/v1", Model: "test-model", APIKey: "sk-test-key"}
}

func (h *harness) prompt(sessionID, title, text string) error {
	return h.svc.Prompt(sessionID, title, text, h.cfg())
}

// history returns the transcript of a session.
func (h *harness) history(sessionID string) []chatMessage {
	h.svc.mu.Lock()
	defer h.svc.mu.Unlock()
	c := h.svc.convs[sessionID]
	if c == nil {
		return nil
	}
	return append([]chatMessage(nil), c.messages...)
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

// waitFor polls until the condition holds.
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// --- tests ---

// A plain answer streams to the UI as deltas and closes with one done event.
func TestPromptStreamsTextAndClosesOnce(t *testing.T) {
	h := newHarness(t, []fakeTurn{{chunks: []string{"Disk ", "looks ", "fine."}}}, nil)

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
	h := newHarness(t, []fakeTurn{
		{chunks: []string{"checking"}, calls: []schema.ToolCall{toolCallFor("call_1", toolBash, `{"command":"df -h"}`)}},
		{chunks: []string{"all good"}},
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
	if tools[0].CallID != "call_1" {
		t.Fatalf("tool call id = %q, want call_1", tools[0].CallID)
	}
	if got := h.sink.text(); got != "checkingall good" {
		t.Fatalf("text across turns = %q", got)
	}

	// The panel sees the answer, then the tool, then the answer that followed
	// the tool, and only then the closing done event.
	var order []string
	for _, e := range h.sink.snapshot() {
		order = append(order, e.name)
	}
	want := []string{EventDelta, EventTool, EventDelta, EventDone}
	if len(order) != len(want) {
		t.Fatalf("event order = %v, want %v", order, want)
	}
	for i := range want {
		if order[i] != want[i] {
			t.Fatalf("event order = %v, want %v", order, want)
		}
	}

	// The follow-up request must carry the assistant's tool call and the tool
	// result, otherwise the endpoint rejects the transcript.
	last := h.model.request(1)
	var sawCall, sawResult bool
	for _, m := range last {
		if m.Role == schema.Assistant && len(m.ToolCalls) == 1 {
			sawCall = true
		}
		if m.Role == schema.Tool && m.ToolCallID == "call_1" && strings.Contains(m.Content, "/dev/sda1") {
			sawResult = true
		}
	}
	if !sawCall || !sawResult {
		t.Fatalf("second request missing tool pairing: %+v", last)
	}
}

// The transcript keeps the tool pairing after the run, so the next prompt
// replays a conversation the endpoint accepts.
func TestToolCallIsAppendedToHistory(t *testing.T) {
	h := newHarness(t, []fakeTurn{
		{calls: []schema.ToolCall{toolCallFor("call_1", toolBash, `{"command":"df -h"}`)}},
		{chunks: []string{"done"}},
	}, nil)

	if err := h.prompt("s1", "host", "check disk"); err != nil {
		t.Fatalf("Prompt: %v", err)
	}
	h.waitDone(t, 1)

	history := h.history("s1")
	var sawAssistant, sawTool bool
	for _, m := range history {
		if m.Role == roleAssistant && len(m.ToolCalls) == 1 && m.ToolCalls[0].ID == "call_1" {
			sawAssistant = true
		}
		if m.Role == roleTool && m.ToolCallID == "call_1" {
			sawTool = true
		}
	}
	if !sawAssistant || !sawTool {
		t.Fatalf("history lost the tool pairing: %+v", history)
	}
}

// A sessionId the model stuffed into the tool arguments is ignored: the tool
// always addresses the session Prompt named.
func TestToolCallIgnoresSessionIdInArguments(t *testing.T) {
	h := newHarness(t, []fakeTurn{
		{calls: []schema.ToolCall{toolCallFor("call_1", toolBash, `{"command":"id","sessionId":"other-host"}`)}},
		{chunks: []string{"ok"}},
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
	h := newHarness(t, []fakeTurn{
		{calls: []schema.ToolCall{toolCallFor("call_1", toolBash, `{"command":"sleep 1","timeoutMs":600000}`)}},
		{chunks: []string{"done"}},
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
	h := newHarness(t, []fakeTurn{
		{calls: []schema.ToolCall{toolCallFor("call_1", toolBash, `{"command":"cat /root/x"}`)}},
		{chunks: []string{"permission denied"}},
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
	var fed bool
	for _, m := range h.model.request(1) {
		if m.Role == schema.Tool && strings.HasPrefix(m.Content, "error:") {
			fed = true
		}
	}
	if !fed {
		t.Fatal("the tool failure was not fed back to the model")
	}
}

// Several tool calls in one turn run one after another, in the order the
// model asked for them.
func TestMultipleToolCallsRunInOrder(t *testing.T) {
	h := newHarness(t, []fakeTurn{
		{calls: []schema.ToolCall{
			toolCallFor("call_1", toolBash, `{"command":"first"}`),
			toolCallFor("call_2", toolBash, `{"command":"second"}`),
			toolCallFor("call_3", toolSftpRead, `{"path":"/etc/hosts"}`),
		}},
		{chunks: []string{"all done"}},
	}, nil)

	if err := h.prompt("s1", "host", "do three things"); err != nil {
		t.Fatalf("Prompt: %v", err)
	}
	h.waitDone(t, 1)

	calls := h.tools.recorded()
	if len(calls) != 3 {
		t.Fatalf("tool calls = %d, want 3", len(calls))
	}
	want := []string{toolBash, toolBash, toolSftpRead}
	for i, name := range want {
		if calls[i].name != name {
			t.Fatalf("tool %d = %q, want %q", i, calls[i].name, name)
		}
	}
	if calls[0].command != "first" || calls[1].command != "second" {
		t.Fatalf("tools ran out of order: %+v", calls)
	}
	tools := h.sink.tools()
	if len(tools) != 3 {
		t.Fatalf("tool events = %+v, want 3", tools)
	}
	for i, id := range []string{"call_1", "call_2", "call_3"} {
		if tools[i].CallID != id {
			t.Fatalf("tool event %d = %q, want %q", i, tools[i].CallID, id)
		}
	}
}

// A denied sensitive tool must not touch the remote host; the denial is a
// failed tool result so the model can stop, not a run-level error.
func TestDeniedSensitiveToolDoesNotExec(t *testing.T) {
	auth := permission.NewService(permission.ServiceDeps{Gate: permission.DenyGate{}})
	h := newHarness(t, []fakeTurn{
		{calls: []schema.ToolCall{toolCallFor("call_1", toolBash, `{"command":"rm -rf /"}`)}},
		{chunks: []string{"blocked"}},
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
	h := newHarness(t, []fakeTurn{
		{calls: []schema.ToolCall{toolCallFor("call_1", toolSftpRead, `{"path":"/etc/hosts"}`)}},
		{chunks: []string{"ok"}},
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
	h := newHarness(t, []fakeTurn{
		{calls: []schema.ToolCall{toolCallFor("call_1", toolSftpWrite, `{"path":"/tmp/x","content":"SECRET"}`)}},
		{chunks: []string{"no"}},
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
	h := newHarness(t, []fakeTurn{
		{calls: []schema.ToolCall{toolCallFor("call_1", toolSftpRead, `{"path":"/etc/hosts"}`)}},
		{calls: []schema.ToolCall{toolCallFor("call_2", toolSftpWrite, `{"path":"/tmp/out","content":"hello"}`)}},
		{chunks: []string{"written"}},
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
	h := newHarness(t, []fakeTurn{
		{calls: []schema.ToolCall{toolCallFor("call_1", toolBash, `{"command":"tail -f /var/log/syslog"}`)}},
	}, nil)
	h.tools.block = true

	if err := h.prompt("s1", "host", "tail the log"); err != nil {
		t.Fatalf("Prompt: %v", err)
	}
	waitFor(t, "the tool to start", func() bool { return len(h.tools.recorded()) > 0 })
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

// Aborting in the middle of an answer closes the run as aborted and reports
// no error: the user asked for the stop.
func TestAbortMidStreamReportsAborted(t *testing.T) {
	h := newHarness(t, []fakeTurn{
		{chunks: []string{"partial "}, block: true},
	}, nil)

	if err := h.prompt("s1", "host", "tell me a story"); err != nil {
		t.Fatalf("Prompt: %v", err)
	}
	waitFor(t, "the first delta", func() bool { return h.sink.count(EventDelta) > 0 })
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
// unanswered: the transcript must still be one the endpoint accepts, because
// an assistant tool_calls message without a result for every call is
// rejected — the abort would otherwise break the whole conversation instead
// of just ending the run.
func TestAbortMidToolLeavesUsableTranscript(t *testing.T) {
	h := newHarness(t, []fakeTurn{
		{calls: []schema.ToolCall{
			toolCallFor("call_1", toolBash, `{"command":"tail -f log"}`),
			toolCallFor("call_2", toolBash, `{"command":"uptime"}`),
		}},
		{chunks: []string{"back again"}},
	}, nil)
	h.tools.block = true

	if err := h.prompt("s1", "host", "tail it"); err != nil {
		t.Fatalf("Prompt: %v", err)
	}
	waitFor(t, "the tool to start", func() bool { return len(h.tools.recorded()) > 0 })
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
	msgs := h.model.request(h.model.callCount() - 1)
	answered := map[string]bool{}
	var pending []string
	for _, m := range msgs {
		if m.Role == schema.Tool {
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
	h := newHarness(t, []fakeTurn{
		{calls: []schema.ToolCall{toolCallFor("call_1", toolBash, `{"command":"sleep"}`)}},
	}, nil)
	h.tools.block = true

	if err := h.prompt("s1", "host", "first"); err != nil {
		t.Fatalf("Prompt: %v", err)
	}
	waitFor(t, "the first run", func() bool { return len(h.tools.recorded()) > 0 })
	if err := h.prompt("s1", "host", "second"); err == nil {
		t.Fatal("a second prompt during a run must be rejected")
	}
	h.svc.Abort("s1")
	h.waitDone(t, 1)
}

// Dispose (session:closed) cancels the run and drops the transcript, so a
// reconnect under the same id never inherits the old conversation.
func TestDisposeCancelsRunAndDropsHistory(t *testing.T) {
	h := newHarness(t, []fakeTurn{
		{calls: []schema.ToolCall{toolCallFor("call_1", toolBash, `{"command":"sleep"}`)}},
		{chunks: []string{"fresh"}},
	}, nil)
	h.tools.block = true

	if err := h.prompt("s1", "host", "remember this"); err != nil {
		t.Fatalf("Prompt: %v", err)
	}
	waitFor(t, "the run to start", func() bool { return len(h.tools.recorded()) > 0 })
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

	for _, m := range h.model.request(h.model.callCount() - 1) {
		if strings.Contains(m.Content, "remember this") {
			t.Fatalf("disposed transcript leaked into a later run: %+v", m)
		}
	}
}

// Clear detaches the in-flight conversation so the next prompt cannot replay
// the emptied history, even if the cancelled run later tries to append.
func TestClearDuringRunDropsHistory(t *testing.T) {
	h := newHarness(t, []fakeTurn{
		{calls: []schema.ToolCall{toolCallFor("call_1", toolBash, `{"command":"sleep"}`)}},
		{chunks: []string{"fresh"}},
	}, nil)
	h.tools.block = true

	if err := h.prompt("s1", "host", "remember this"); err != nil {
		t.Fatalf("Prompt: %v", err)
	}
	waitFor(t, "the run to start", func() bool { return len(h.tools.recorded()) > 0 })
	h.svc.Clear("s1")

	h.tools.mu.Lock()
	h.tools.block = false
	h.tools.mu.Unlock()
	if err := h.prompt("s1", "host", "new question"); err != nil {
		t.Fatalf("Prompt after clear must not wait on the detached run: %v", err)
	}
	h.waitDone(t, 1)

	for _, m := range h.model.request(h.model.callCount() - 1) {
		if strings.Contains(m.Content, "remember this") {
			t.Fatalf("cleared transcript leaked into a later run: %+v", m)
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
	h := newHarness(t, []fakeTurn{
		{calls: []schema.ToolCall{toolCallFor("call_1", toolBash, `{"command":"sleep"}`)}},
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
	waitFor(t, "the run to start", func() bool { return len(h.tools.recorded()) > 0 })

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
	turns := make([]fakeTurn, totalToolTurns+1)
	for i := 0; i < totalToolTurns; i++ {
		turns[i] = fakeTurn{calls: []schema.ToolCall{
			toolCallFor(fmt.Sprintf("call_%d", i+1), toolBash, fmt.Sprintf(`{"command":"echo step %d"}`, i+1)),
		}}
	}
	turns[totalToolTurns] = fakeTurn{chunks: []string{"all 12 steps completed successfully"}}

	h := newHarness(t, turns, nil) // Deps.MaxTurns defaults to 0 (Eino's own budget)
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
	turns := make([]fakeTurn, totalTurns)
	for i := range turns {
		turns[i] = fakeTurn{calls: []schema.ToolCall{
			toolCallFor(fmt.Sprintf("call_%d", i+1), toolBash, fmt.Sprintf(`{"command":"echo step %d"}`, i+1)),
		}}
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
	turns := make([]fakeTurn, 6)
	for i := range turns {
		turns[i] = fakeTurn{calls: []schema.ToolCall{
			toolCallFor(fmt.Sprintf("call_%d", i+1), toolBash, `{"command":"echo loop"}`),
		}}
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

// The reasoning of the model is kept in the transcript but never rendered.
func TestReasoningContentIsKeptButNotRendered(t *testing.T) {
	h := newHarness(t, []fakeTurn{
		{chunks: []string{"the answer"}, reasoning: "let me think about it"},
		{chunks: []string{"and again"}},
	}, nil)

	if err := h.prompt("s1", "host", "think"); err != nil {
		t.Fatalf("Prompt: %v", err)
	}
	h.waitDone(t, 1)

	if strings.Contains(h.sink.text(), "let me think") {
		t.Fatalf("the reasoning leaked into the panel: %q", h.sink.text())
	}
	var reasoning string
	for _, m := range h.history("s1") {
		if m.Role == roleAssistant && m.ReasoningContent != "" {
			reasoning = m.ReasoningContent
		}
	}
	if reasoning != "let me think about it" {
		t.Fatalf("reasoning content = %q, want it kept in the transcript", reasoning)
	}

	// The next prompt replays the reasoning to the provider.
	if err := h.prompt("s1", "host", "and now?"); err != nil {
		t.Fatalf("Prompt: %v", err)
	}
	h.waitDone(t, 2)
	var replayed bool
	for _, m := range h.model.request(1) {
		if m.Role == schema.Assistant && m.ReasoningContent == "let me think about it" {
			replayed = true
		}
	}
	if !replayed {
		t.Fatal("the reasoning was not replayed to the model")
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
	h := newHarness(t, []fakeTurn{{chunks: []string{"hi"}}}, nil)
	h.svc.DisposeAll()
	if err := h.prompt("s1", "host", "hello"); err == nil {
		t.Fatal("Prompt after DisposeAll must be rejected")
	}
}

// A tool result larger than the transcript cap is truncated with a marker, so
// one noisy command cannot grow the next request without bound.
func TestOversizedToolResultTruncated(t *testing.T) {
	h := newHarness(t, []fakeTurn{
		{calls: []schema.ToolCall{toolCallFor("call_1", toolBash, `{"command":"cat big"}`)}},
		{chunks: []string{"ok"}},
	}, nil)
	h.tools.out = strings.Repeat("y", MaxToolResultBytes*2)

	if err := h.prompt("s1", "host", "dump it"); err != nil {
		t.Fatalf("Prompt: %v", err)
	}
	h.waitDone(t, 1)

	for _, m := range h.model.request(1) {
		if m.Role != schema.Tool {
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

// reconcile answers the tool calls a stopped run left open, so the next
// prompt replays a transcript the endpoint accepts.
func TestReconcileAnswersUnansweredToolCalls(t *testing.T) {
	c := &conversation{messages: []chatMessage{
		{Role: roleUser, Content: "run it"},
		{Role: roleAssistant, ToolCalls: []toolCall{
			{ID: "call_1"},
			{ID: "call_2"},
		}},
		{Role: roleTool, ToolCallID: "call_1", Content: "ok"},
	}}
	svc := New(Deps{})
	defer svc.DisposeAll()
	svc.reconcile(c)

	var answered []string
	for _, m := range c.messages {
		if m.Role == roleTool {
			answered = append(answered, m.ToolCallID)
		}
	}
	if len(answered) != 2 || answered[1] != "call_2" {
		t.Fatalf("answered calls = %v, want call_1 and call_2", answered)
	}
	if !strings.Contains(c.messages[len(c.messages)-1].Content, "stopped before") {
		t.Fatalf("the unanswered call was not reconciled: %+v", c.messages)
	}
}

// The system prompt pins the session's host and states the no-local-machine
// boundary; the advertised tools are exactly the remote four.
func TestSystemPromptAndToolSurface(t *testing.T) {
	h := newHarness(t, []fakeTurn{{chunks: []string{"hi"}}}, nil)
	if err := h.prompt("s1", "prod-web", "hello"); err != nil {
		t.Fatalf("Prompt: %v", err)
	}
	h.waitDone(t, 1)

	first := h.model.request(0)
	if len(first) == 0 || first[0].Role != schema.System {
		t.Fatalf("first message = %+v, want the system prompt", first)
	}
	system := first[0].Content
	if !strings.Contains(system, "prod-web") {
		t.Fatalf("system prompt does not name the host: %q", system)
	}
	if !strings.Contains(system, "cannot see the user's local machine") {
		t.Fatalf("system prompt drops the local-machine boundary: %q", system)
	}

	tools := h.model.advertisedTools()
	want := map[string]bool{toolBash: true, toolSftpList: true, toolSftpRead: true, toolSftpWrite: true}
	if len(tools) != len(want) {
		t.Fatalf("advertised %d tools, want %d", len(tools), len(want))
	}
	for _, info := range tools {
		if !want[info.Name] {
			t.Fatalf("unexpected tool advertised: %q", info.Name)
		}
		if _, err := info.ParamsOneOf.ToJSONSchema(); err != nil {
			t.Fatalf("%s has no parameter schema: %v", info.Name, err)
		}
	}
}

// A hallucinated tool is refused as a failed call: the model gets the
// refusal as text and the run continues.
func TestUnknownToolIsRefusedWithoutEndingTheRun(t *testing.T) {
	h := newHarness(t, []fakeTurn{
		{calls: []schema.ToolCall{toolCallFor("call_1", "rm_rf", `{"path":"/"}`)}},
		{chunks: []string{"I cannot do that"}},
	}, nil)

	if err := h.prompt("s1", "host", "wipe it"); err != nil {
		t.Fatalf("Prompt: %v", err)
	}
	h.waitDone(t, 1)

	if calls := h.tools.recorded(); len(calls) != 0 {
		t.Fatalf("an unknown tool must not reach the remote host: %+v", calls)
	}
	tools := h.sink.tools()
	if len(tools) != 1 || tools[0].OK || tools[0].Name != "rm_rf" {
		t.Fatalf("tool event = %+v", tools)
	}
	if errs := h.sink.errors(); len(errs) != 0 {
		t.Fatalf("an unknown tool must not fail the run: %v", errs)
	}
	if got := h.sink.text(); got != "I cannot do that" {
		t.Fatalf("streamed text = %q, want the model's answer", got)
	}
}

// A stalled provider is cut off by the request timeout instead of hanging the
// panel forever.
func TestRequestTimeoutEndsTheRun(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(300 * time.Millisecond)
	}))
	defer srv.Close()

	sink := &recSink{}
	svc := New(Deps{
		Tools:          &fakeTools{},
		Sink:           sink,
		RequestTimeout: 100 * time.Millisecond,
	})
	defer svc.DisposeAll()

	cfg := Config{BaseURL: srv.URL, Model: "m", APIKey: "sk-test-key"}
	if err := svc.Prompt("s1", "host", "hello", cfg); err != nil {
		t.Fatalf("Prompt: %v", err)
	}
	waitFor(t, "the failed run to close", func() bool { return sink.count(EventDone) == 1 })

	errs := sink.errors()
	if len(errs) != 1 {
		t.Fatalf("error events = %+v, want one", errs)
	}
	if errs[0].Error.Code != apperror.Timeout {
		t.Fatalf("error code = %q, want %q", errs[0].Error.Code, apperror.Timeout)
	}
}

// The request the model sees stays within the history cap, even when a long
// run produces far more messages than the cap allows.
func TestModelRequestStaysWithinHistoryCap(t *testing.T) {
	const toolTurns = 25
	scripted := make([]fakeTurn, toolTurns+1)
	for i := range scripted {
		scripted[i] = fakeTurn{calls: []schema.ToolCall{
			toolCallFor(fmt.Sprintf("call_%d", i+1), toolBash, `{"command":"echo"}`),
		}}
	}
	scripted[toolTurns] = fakeTurn{chunks: []string{"done"}}

	h := newHarness(t, scripted, func(d *Deps) { d.MaxTurns = toolTurns + 1 })
	if err := h.prompt("s1", "host", "loop a lot"); err != nil {
		t.Fatalf("Prompt: %v", err)
	}
	h.waitDone(t, 1)

	last := h.model.request(h.model.callCount() - 1)
	if len(last) > MaxHistoryMessages+1 {
		t.Fatalf("request carries %d messages, want at most %d", len(last), MaxHistoryMessages+1)
	}
	if len(last) == 0 || last[0].Role != schema.System {
		t.Fatalf("the system prompt was cut away: %+v", last)
	}
	// Every tool call the request carries must still have its result.
	answered := map[string]bool{}
	for _, m := range last {
		if m.Role == schema.Tool {
			answered[m.ToolCallID] = true
		}
	}
	for _, m := range last {
		for _, call := range m.ToolCalls {
			if !answered[call.ID] {
				t.Fatalf("tool call %q lost its result in the trimmed request", call.ID)
			}
		}
	}
}

// An endpoint that cannot be reached is a coded failure, not a crash.
func TestUnreachableEndpointIsCoded(t *testing.T) {
	sink := &recSink{}
	svc := New(Deps{Tools: &fakeTools{}, Sink: sink})
	defer svc.DisposeAll()

	cfg := Config{BaseURL: "http://127.0.0.1:1/v1", Model: "m", APIKey: "sk-test-key"}
	if err := svc.Prompt("s1", "host", "hello", cfg); err != nil {
		t.Fatalf("Prompt: %v", err)
	}
	waitFor(t, "the failed run to close", func() bool { return sink.count(EventDone) == 1 })

	errs := sink.errors()
	if len(errs) != 1 {
		t.Fatalf("error events = %+v, want one", errs)
	}
	if errs[0].Error.Code != apperror.Unknown {
		t.Fatalf("error code = %q, want %q", errs[0].Error.Code, apperror.Unknown)
	}
	if !strings.Contains(errs[0].Error.Message, "unreachable") {
		t.Fatalf("error message = %q, want the unreachable endpoint", errs[0].Error.Message)
	}
}

// --- the real provider path ---

// providerServer is a minimal OpenAI-compatible endpoint: it records the
// request and answers with the scripted SSE frames.
type providerServer struct {
	mu       sync.Mutex
	requests []string
	auths    []string
	bodies   []string
	frames   []string
	status   int
	body     string
}

func (p *providerServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	raw, _ := io.ReadAll(r.Body)
	p.mu.Lock()
	p.requests = append(p.requests, r.URL.Path)
	p.auths = append(p.auths, r.Header.Get("Authorization"))
	p.bodies = append(p.bodies, string(raw))
	status, body, frames := p.status, p.body, p.frames
	p.mu.Unlock()

	if status != 0 {
		w.WriteHeader(status)
		fmt.Fprint(w, body)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	flusher, _ := w.(http.Flusher)
	for _, frame := range frames {
		fmt.Fprint(w, frame)
		if flusher != nil {
			flusher.Flush()
		}
	}
	fmt.Fprint(w, "data: [DONE]\n\n")
}

func (p *providerServer) recorded() ([]string, []string, []string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]string(nil), p.requests...),
		append([]string(nil), p.auths...),
		append([]string(nil), p.bodies...)
}

// The API key goes into the Authorization header and nowhere else: not into
// the request body, and not into any event the WebView can read.
func TestAPIKeyOnlyTravelsInAuthorizationHeader(t *testing.T) {
	prov := &providerServer{frames: []string{
		"data: {\"choices\":[{\"delta\":{\"role\":\"assistant\",\"content\":\"hi\"}}]}\n\n",
	}}
	srv := httptest.NewServer(prov)
	defer srv.Close()

	sink := &recSink{}
	svc := New(Deps{Tools: &fakeTools{}, Sink: sink})
	defer svc.DisposeAll()

	cfg := Config{BaseURL: srv.URL + "/v1", Model: "test-model", APIKey: "sk-test-key"}
	if err := svc.Prompt("s1", "host", "hello", cfg); err != nil {
		t.Fatalf("Prompt: %v", err)
	}
	waitFor(t, "the run to close", func() bool { return sink.count(EventDone) == 1 })

	paths, auths, bodies := prov.recorded()
	if len(paths) != 1 || paths[0] != "/v1/chat/completions" {
		t.Fatalf("request paths = %v, want /v1/chat/completions", paths)
	}
	if len(auths) != 1 || auths[0] != "Bearer sk-test-key" {
		t.Fatalf("authorization headers = %v", auths)
	}
	if strings.Contains(strings.Join(bodies, ""), "sk-test-key") {
		t.Fatal("the API key must not appear in the request body")
	}
	events, _ := json.Marshal(sink.snapshot())
	if strings.Contains(string(events), "sk-test-key") {
		t.Fatal("the API key must not appear in any emitted event")
	}
	if got := sink.text(); got != "hi" {
		t.Fatalf("streamed text = %q, want hi", got)
	}
}

// An endpoint that echoes the key in an error must not put it on screen.
func TestProviderErrorRedactsKeyAndCodesTheFailure(t *testing.T) {
	prov := &providerServer{
		status: http.StatusUnauthorized,
		body:   `{"error":{"message":"Incorrect API key provided: sk-test-key"}}`,
	}
	srv := httptest.NewServer(prov)
	defer srv.Close()

	sink := &recSink{}
	svc := New(Deps{Tools: &fakeTools{}, Sink: sink})
	defer svc.DisposeAll()

	cfg := Config{BaseURL: srv.URL, Model: "m", APIKey: "sk-test-key"}
	if err := svc.Prompt("s1", "host", "hello", cfg); err != nil {
		t.Fatalf("Prompt: %v", err)
	}
	waitFor(t, "the failed run to close", func() bool { return sink.count(EventDone) == 1 })

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
	if sink.count(EventDone) != 1 {
		t.Fatalf("a failed run must still close with one done event, got %d", sink.count(EventDone))
	}
}

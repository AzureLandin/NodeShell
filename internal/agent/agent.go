// Package agent runs a small tool-calling assistant against one already
// connected SSH session. It owns no transport of its own: the LLM is reached
// over an OpenAI-compatible HTTP endpoint, and every tool the model may call
// is served through BoundCaller → mcpcli.Runtime.Call, with the current
// session injected so the assistant can only ever touch the remote host the
// user is already looking at.
//
// The package deliberately implements only the loop (prompt -> stream ->
// tools -> stream) and none of the extras a full coding agent ships: no local
// filesystem access, no skills or extensions, no sub-agents and no context
// compaction beyond a plain transcript cap.
package agent

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"nodeshell/internal/apperror"
	"nodeshell/internal/mcpcli"
)

// Event names crossing the Wails boundary (mirrored by IPC in
// src/shared/types.ts).
const (
	EventDelta = "agent:delta"
	EventTool  = "agent:tool"
	EventDone  = "agent:done"
	EventError = "agent:error"
)

// Loop and payload limits. They bound both what one run may cost and what one
// run may put back into the transcript, so a talkative model can neither spin
// forever nor grow the request without end.
const (
	// DefaultMaxTurns is the number of assistant turns (LLM calls) one prompt
	// may consume before the run is stopped with an observable error.
	DefaultMaxTurns = 8
	// DefaultExecTimeout bounds one bash tool call.
	DefaultExecTimeout = 60 * time.Second
	// DefaultRequestTimeout bounds one streamed LLM request.
	DefaultRequestTimeout = 120 * time.Second
	// MaxPromptBytes bounds one user prompt.
	MaxPromptBytes = 32 * 1024
	// MaxToolResultBytes bounds what one tool result contributes to the
	// transcript; longer output is truncated with a visible marker so the
	// model knows it is seeing a prefix.
	MaxToolResultBytes = 16 * 1024
	// MaxHistoryMessages bounds the transcript replayed to the model.
	MaxHistoryMessages = 40
	// MaxFileBytes is the remote text cap advertised to the model. The MCP
	// runtime enforces the same limit on read/write.
	MaxFileBytes = mcpcli.MaxFileBytes
)

// EventSink is the event emission seam; production emits through the Wails
// runtime, tests record. A nil sink is a no-op.
type EventSink interface {
	Emit(event string, payload any)
}

type nopSink struct{}

func (nopSink) Emit(string, any) {}

// Config is the resolved endpoint configuration for one run. APIKey never
// leaves this struct: it is only ever written to an outgoing Authorization
// header, never to an event, an error message or the transcript.
type Config struct {
	BaseURL string
	Model   string
	APIKey  string
}

// ErrNotConfigured is the pre-flight rejection for a missing API key or
// model. It is returned before a run starts, so the UI can point the user at
// settings instead of showing a failed run.
var ErrNotConfigured = &Error{Code: apperror.Unknown, Message: "Agent is not configured"}

// Error carries the stable code the frontend maps onto AppError.code.
// Messages are generic: no API key, no remote path and no raw command output.
type Error struct {
	Code    string
	Message string
}

func (e *Error) Error() string { return e.Message }

// ErrorCode lets apperror.Format carry the stable code across IPC.
func (e *Error) ErrorCode() string { return e.Code }

func errf(code, format string, args ...any) error {
	return &Error{Code: code, Message: fmt.Sprintf(format, args...)}
}

// AppError is the coded error shape carried by agent:error (matches
// AppError in src/shared/types.ts).
type AppError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// DeltaEvent is one streamed assistant text fragment.
type DeltaEvent struct {
	SessionID string `json:"sessionId"`
	Delta     string `json:"delta"`
}

// ToolEvent reports one finished tool call. Summary is a short, display-ready
// line (the command or path); Detail is the first line of a failure, never a
// stack trace or a secret.
type ToolEvent struct {
	SessionID string `json:"sessionId"`
	CallID    string `json:"callId"`
	Name      string `json:"name"`
	Summary   string `json:"summary"`
	OK        bool   `json:"ok"`
	Detail    string `json:"detail,omitempty"`
}

// DoneEvent closes one run; exactly one is emitted per accepted prompt.
type DoneEvent struct {
	SessionID string `json:"sessionId"`
	Aborted   bool   `json:"aborted"`
}

// ErrorEvent reports a failure after the prompt was accepted. It is always
// followed by a DoneEvent.
type ErrorEvent struct {
	SessionID string   `json:"sessionId"`
	Error     AppError `json:"error"`
}

// Deps wires a Service. Zero timeouts and turn counts fall back to the
// package defaults; tests inject shorter ones.
type Deps struct {
	Tools          ToolCaller
	Sink           EventSink
	Client         *http.Client
	MaxTurns       int
	ExecTimeout    time.Duration
	RequestTimeout time.Duration
}

// conversation is the per-session transcript plus the handle on its in-flight
// run. Only one run may be active per session: a second prompt is rejected
// rather than interleaved, so the transcript can never be appended to by two
// loops at once. Clear/Dispose replace or delete this pointer so an in-flight
// run cannot write back into the session the user just emptied.
type conversation struct {
	messages []chatMessage
	running  bool
	cancel   context.CancelFunc
	// done is closed when the run goroutine returns. DisposeAll waits on it
	// so SSH teardown cannot race an in-flight bash/sftp_write.
	done chan struct{}
}

// Service runs at most one agent loop per SSH session. State is per session
// and dropped when that session closes, so a torn-down SSH connection can
// never leave a transcript or a running loop behind.
type Service struct {
	tools          ToolCaller
	sink           EventSink
	client         *http.Client
	maxTurns       int
	execTimeout    time.Duration
	requestTimeout time.Duration

	// mu guards convs and closed. It is never held while an LLM request runs,
	// a tool executes or an event is emitted.
	mu     sync.Mutex
	convs  map[string]*conversation
	closed bool
}

// New builds a Service.
func New(d Deps) *Service {
	if d.Sink == nil {
		d.Sink = nopSink{}
	}
	if d.Client == nil {
		// No client-level timeout: each request is bounded by its own context
		// so a long stream is not cut off mid-answer.
		d.Client = &http.Client{}
	}
	if d.MaxTurns <= 0 {
		d.MaxTurns = DefaultMaxTurns
	}
	if d.ExecTimeout <= 0 {
		d.ExecTimeout = DefaultExecTimeout
	}
	if d.RequestTimeout <= 0 {
		d.RequestTimeout = DefaultRequestTimeout
	}
	return &Service{
		tools:          d.Tools,
		sink:           d.Sink,
		client:         d.Client,
		maxTurns:       d.MaxTurns,
		execTimeout:    d.ExecTimeout,
		requestTimeout: d.RequestTimeout,
		convs:          make(map[string]*conversation),
	}
}

// Prompt accepts one user message for the session and starts a run in the
// background. cfg is the already-resolved endpoint (provider + model + key)
// for this send; switching models between prompts keeps the same transcript.
// It returns an error only for a pre-flight rejection (empty or oversized
// prompt, missing configuration, a run already in flight); once accepted,
// progress and failures are reported through the events, and exactly one
// DoneEvent closes the run.
func (s *Service) Prompt(sessionID, title, text string, cfg Config) error {
	prompt := strings.TrimSpace(text)
	if sessionID == "" {
		return errf(apperror.SessionNotFound, "Session not found")
	}
	if prompt == "" {
		return errf(apperror.Unknown, "Prompt is empty")
	}
	if len(prompt) > MaxPromptBytes {
		return errf(apperror.Unknown, "Prompt is too long")
	}
	if strings.TrimSpace(cfg.APIKey) == "" || strings.TrimSpace(cfg.Model) == "" ||
		strings.TrimSpace(cfg.BaseURL) == "" {
		return ErrNotConfigured
	}

	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return errf(apperror.Unknown, "Agent is shutting down")
	}
	c := s.convs[sessionID]
	if c == nil {
		c = &conversation{}
		s.convs[sessionID] = c
	}
	if c.running {
		s.mu.Unlock()
		return errf(apperror.Unknown, "Agent is still working on the previous message")
	}
	ctx, cancel := context.WithCancel(context.Background())
	c.running = true
	c.cancel = cancel
	c.done = make(chan struct{})
	c.messages = trimHistory(append(c.messages, chatMessage{Role: roleUser, Content: prompt}))
	s.mu.Unlock()

	go s.run(ctx, cancel, sessionID, title, cfg, c)
	return nil
}

// Abort cancels the session's in-flight run; the run still emits its
// DoneEvent (with aborted set). Unknown sessions and idle runs are a no-op.
func (s *Service) Abort(sessionID string) {
	s.mu.Lock()
	var cancel context.CancelFunc
	if c := s.convs[sessionID]; c != nil {
		cancel = c.cancel
	}
	s.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

// Clear detaches the session's conversation so an in-flight run cannot write
// assistant/tool messages back into the transcript the user just emptied.
// The next prompt starts from a fresh history; the detached run is cancelled
// and its later events are dropped.
func (s *Service) Clear(sessionID string) {
	s.mu.Lock()
	old := s.convs[sessionID]
	if old == nil {
		s.mu.Unlock()
		return
	}
	s.convs[sessionID] = &conversation{}
	cancel := old.cancel
	s.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

// Dispose forgets the session entirely (its SSH session is gone): the run is
// cancelled and the transcript dropped. Called from the session:closed path,
// so a reconnect never inherits the previous connection's conversation.
func (s *Service) Dispose(sessionID string) {
	s.mu.Lock()
	c := s.convs[sessionID]
	delete(s.convs, sessionID)
	s.mu.Unlock()
	joinRun(c)
}

// DisposeAll cancels every run, joins the run goroutines, and drops every
// transcript (app shutdown). Later prompts are rejected instead of starting a
// run the WebView could never observe.
func (s *Service) DisposeAll() {
	s.mu.Lock()
	s.closed = true
	old := make([]*conversation, 0, len(s.convs))
	for id, c := range s.convs {
		old = append(old, c)
		delete(s.convs, id)
	}
	s.mu.Unlock()
	for _, c := range old {
		joinRun(c)
	}
}

// joinRun cancels an in-flight run and waits for its goroutine so callers
// such as DisposeAll do not tear down SSH under a live tool call.
func joinRun(c *conversation) {
	if c == nil {
		return
	}
	if c.cancel != nil {
		c.cancel()
	}
	if c.done != nil {
		<-c.done
	}
}

// run drives one accepted prompt to completion and always closes it with a
// single DoneEvent. A cancelled run reports aborted instead of an error: the
// user asked for the stop, so it is not a failure.
func (s *Service) run(ctx context.Context, cancel context.CancelFunc, sessionID, title string, cfg Config, c *conversation) {
	defer func() {
		cancel()
		if c.done != nil {
			close(c.done)
		}
	}()
	err := s.loop(ctx, sessionID, title, cfg, c)
	s.reconcile(c)
	aborted := ctx.Err() != nil
	if err != nil && !aborted {
		s.emitIfCurrent(sessionID, c, EventError, ErrorEvent{SessionID: sessionID, Error: AppError{
			Code:    errorCode(err),
			Message: err.Error(),
		}})
	}
	s.mu.Lock()
	if cur := s.convs[sessionID]; cur == c {
		c.running = false
		c.cancel = nil
	}
	s.mu.Unlock()
	s.emitIfCurrent(sessionID, c, EventDone, DoneEvent{SessionID: sessionID, Aborted: aborted})
}

// loop alternates streamed assistant turns and tool execution until the model
// answers without tool calls. Reaching the turn limit is an observable error,
// never a silent stop.
func (s *Service) loop(ctx context.Context, sessionID, title string, cfg Config, c *conversation) error {
	for turn := 0; turn < s.maxTurns; turn++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		msgs := s.snapshot(c, title)
		reqCtx, cancelReq := context.WithTimeout(ctx, s.requestTimeout)
		res, err := s.stream(reqCtx, cfg, msgs, func(delta string) {
			s.emitIfCurrent(sessionID, c, EventDelta, DeltaEvent{SessionID: sessionID, Delta: delta})
		})
		cancelReq()
		if err != nil {
			return err
		}
		s.append(c, assistantMessage(res))
		if len(res.toolCalls) == 0 {
			return nil
		}
		for _, call := range res.toolCalls {
			if err := ctx.Err(); err != nil {
				return err
			}
			out, summary, ok := s.runTool(ctx, sessionID, title, call)
			detail := ""
			if !ok {
				detail = firstLine(out)
			}
			s.emitIfCurrent(sessionID, c, EventTool, ToolEvent{
				SessionID: sessionID,
				CallID:    call.ID,
				Name:      call.Function.Name,
				Summary:   summary,
				OK:        ok,
				Detail:    detail,
			})
			s.append(c, chatMessage{Role: roleTool, ToolCallID: call.ID, Content: out})
		}
	}
	return errf(apperror.Unknown, "Agent stopped after %d steps without finishing", s.maxTurns)
}

// emitIfCurrent drops events for a run that has been Clear'd, Dispose'd or
// shut down, so a detached loop cannot refill the UI or emit after the
// WebView is gone. The closed/current check is released before Emit, matching
// the "mu is never held while an event is emitted" rule.
func (s *Service) emitIfCurrent(sessionID string, c *conversation, event string, payload any) {
	s.mu.Lock()
	live := !s.closed && s.convs[sessionID] == c
	s.mu.Unlock()
	if live {
		s.sink.Emit(event, payload)
	}
}

// snapshot returns the system prompt followed by a copy of the transcript, so
// the request is built from a stable view even if the conversation is cleared
// while the request is in flight.
func (s *Service) snapshot(c *conversation, title string) []chatMessage {
	s.mu.Lock()
	defer s.mu.Unlock()
	msgs := make([]chatMessage, 0, len(c.messages)+1)
	msgs = append(msgs, chatMessage{Role: roleSystem, Content: systemPrompt(title)})
	return append(msgs, c.messages...)
}

func (s *Service) append(c *conversation, msg chatMessage) {
	s.mu.Lock()
	defer s.mu.Unlock()
	c.messages = trimHistory(append(c.messages, msg))
}

// reconcile answers any tool call the run left open. A run stopped between the
// assistant's tool call and its result (abort, endpoint failure, step limit)
// would otherwise leave an unanswered tool_calls message in the transcript,
// and the API rejects that on the *next* prompt ??so the abort would break the
// conversation instead of just ending the run.
func (s *Service) reconcile(c *conversation) {
	s.mu.Lock()
	defer s.mu.Unlock()
	last := -1
	for i := len(c.messages) - 1; i >= 0; i-- {
		if c.messages[i].Role == roleAssistant && len(c.messages[i].ToolCalls) > 0 {
			last = i
			break
		}
	}
	if last < 0 {
		return
	}
	answered := make(map[string]bool)
	for _, m := range c.messages[last+1:] {
		if m.Role == roleTool {
			answered[m.ToolCallID] = true
		}
	}
	for _, call := range c.messages[last].ToolCalls {
		if !answered[call.ID] {
			c.messages = append(c.messages, chatMessage{
				Role:       roleTool,
				ToolCallID: call.ID,
				Content:    "error: the run stopped before this call finished",
			})
		}
	}
	c.messages = trimHistory(c.messages)
}

// trimHistory caps the transcript from the front and then drops any leading
// tool result whose assistant call was cut away: an orphan tool message is
// rejected by the API, so the cap must never produce one.
func trimHistory(msgs []chatMessage) []chatMessage {
	if len(msgs) > MaxHistoryMessages {
		msgs = msgs[len(msgs)-MaxHistoryMessages:]
	}
	for len(msgs) > 0 && msgs[0].Role == roleTool {
		msgs = msgs[1:]
	}
	return msgs
}

// systemPrompt pins the assistant to the remote host of the current session.
// The title is the tab label the user sees, so the model refers to the host
// the same way the UI does.
func systemPrompt(title string) string {
	host := strings.TrimSpace(title)
	if host == "" {
		host = "the connected host"
	}
	return strings.Join([]string{
		"You are the assistant built into NodeShell, an SSH client.",
		"You are working on the remote host \"" + host + "\" through an SSH session the user already opened.",
		"Use the tools to inspect or change that remote host. You cannot see the user's local machine, and every path you pass to a tool is a remote path.",
		"Prefer read-only commands first, and state what a command will change before running anything destructive.",
		"Commands run non-interactively: they cannot prompt, page or wait for input.",
		"Keep answers short and concrete, and reply in the language the user writes in.",
	}, "\n")
}

// errorCode extracts the stable code from a coded error; a cancelled context
// is CANCELLED and a deadline is TIMEOUT so the UI can tell a user stop from
// a stalled endpoint.
func errorCode(err error) string {
	var coded apperror.Coded
	if errors.As(err, &coded) {
		return coded.ErrorCode()
	}
	switch {
	case errors.Is(err, context.Canceled):
		return apperror.Cancelled
	case errors.Is(err, context.DeadlineExceeded):
		return apperror.Timeout
	}
	return apperror.Unknown
}

// firstLine returns the first line of s, bounded, for the tool event detail.
func firstLine(s string) string {
	line := s
	if i := strings.IndexByte(line, '\n'); i >= 0 {
		line = line[:i]
	}
	line = strings.TrimSpace(line)
	const max = 200
	if len(line) > max {
		return line[:max] + "?"
	}
	return line
}

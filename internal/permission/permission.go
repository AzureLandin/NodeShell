// Package permission gates sensitive agent and MCP tool calls. The GUI and
// --mcp processes are separate, so the prompt itself is injected: the GUI
// uses an in-app modal (ChannelGate), MCP uses a native OS dialog
// (NativeGate). A nil Gate allows, which is how existing tests keep running
// without a UI.
package permission

import (
	"context"
	"strings"
	"sync"

	"nodeshell/internal/apperror"
)

// Event names crossing the Wails boundary (GUI only). MCP never emits these:
// it has no WebView, so it prompts through NativeGate instead.
const (
	EventAsk    = "permission:ask"
	EventClosed = "permission:closed"
)

// Source identifies which caller is asking, so the dialog can label it.
const (
	SourceAgent = "agent"
	SourceMCP   = "mcp"
)

// Policy is the persisted setting (settings.permissionPolicy).
type Policy string

const (
	PolicyAsk   Policy = "ask"
	PolicyAllow Policy = "allow"
	PolicyDeny  Policy = "deny"
)

// Decision is one answer to an ask. MCP native dialogs only produce once or
// deny; allow-session is a GUI convenience.
type Decision string

const (
	DecisionDeny         Decision = "deny"
	DecisionAllowOnce    Decision = "allow"
	DecisionAllowSession Decision = "allow-session"
)

// Request is the prompt payload. Summary/Detail must never carry file
// contents, passwords or API keys — only a command, a path, or a byte count.
type Request struct {
	ID        string `json:"id"`
	Source    string `json:"source"`
	Tool      string `json:"tool"`
	SessionID string `json:"sessionId"`
	Title     string `json:"title"`
	Summary   string `json:"summary"`
	Detail    string `json:"detail,omitempty"`
}

// ClosedEvent tells the GUI to drop a prompt that was cancelled (abort,
// session close) rather than answered.
type ClosedEvent struct {
	ID string `json:"id"`
}

// Error is the coded denial returned to the tool caller. The message is
// generic: no command, path or host name, so it is safe to put in a tool
// result or an MCP error string.
type Error struct {
	Code    string
	Message string
}

func (e *Error) Error() string     { return e.Message }
func (e *Error) ErrorCode() string { return e.Code }

// ErrDenied is the stable failure for a rejected or cancelled ask.
var ErrDenied = &Error{Code: apperror.PermissionDenied, Message: "Permission denied"}

// Authorizer is the surface agent and MCP call before a sensitive tool runs.
type Authorizer interface {
	Authorize(ctx context.Context, req Request) error
}

// Gate blocks until the user answers one ask. Production gates honour ctx
// cancellation by returning deny; tests inject an immediate Decision.
type Gate interface {
	Ask(ctx context.Context, req Request) (Decision, error)
}

// Emitter is the event seam ChannelGate uses; production is the Wails sink.
type Emitter interface {
	Emit(event string, payload any)
}

// PolicyFunc is read on every Authorize so a settings change applies to the
// next tool call without rebuilding the service.
type PolicyFunc func() Policy

// ServiceDeps wires a Service.
type ServiceDeps struct {
	Gate   Gate
	Policy PolicyFunc
}

// Service applies the persisted policy, a per-session allowlist, and the
// inner Gate. Nil Gate with policy "ask" allows (tests / headless).
type Service struct {
	gate   Gate
	policy PolicyFunc

	mu      sync.Mutex
	allowed map[string]bool
}

// NewService builds a Service.
func NewService(d ServiceDeps) *Service {
	return &Service{gate: d.Gate, policy: d.Policy, allowed: map[string]bool{}}
}

// Sensitive reports whether the named tool may change the remote host or
// move files. Reads and listings are not gated: the user is already on that
// host (GUI) or already connected the MCP session.
func Sensitive(tool string) bool {
	switch tool {
	case "bash", "sftp_write", "run_command", "sftp_upload", "sftp_download":
		return true
	default:
		return false
	}
}

// ParsePolicy maps a settings value onto a Policy. Unknown or empty values
// fall back to ask, so a corrupt file never silently auto-allows.
func ParsePolicy(value string) Policy {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case string(PolicyAllow):
		return PolicyAllow
	case string(PolicyDeny):
		return PolicyDeny
	default:
		return PolicyAsk
	}
}

// ParseDecision maps a frontend/IPC string onto a Decision. Unknown values
// are rejected rather than treated as allow.
func ParseDecision(value string) (Decision, bool) {
	switch Decision(value) {
	case DecisionDeny, DecisionAllowOnce, DecisionAllowSession:
		return Decision(value), true
	default:
		return DecisionDeny, false
	}
}

// Authorize returns nil when the tool may run, or ErrDenied. Non-sensitive
// tools always pass. A cancelled ctx is a denial, never an execution.
func (s *Service) Authorize(ctx context.Context, req Request) error {
	if s == nil || !Sensitive(req.Tool) {
		return nil
	}
	if err := ctx.Err(); err != nil {
		return ErrDenied
	}
	switch s.currentPolicy() {
	case PolicyAllow:
		return nil
	case PolicyDeny:
		return ErrDenied
	}
	if req.SessionID != "" && s.remembered(req.SessionID, req.Tool) {
		return nil
	}
	if s.gate == nil {
		return nil
	}
	d, err := s.gate.Ask(ctx, req)
	if err != nil {
		return ErrDenied
	}
	switch d {
	case DecisionAllowOnce:
		return nil
	case DecisionAllowSession:
		if req.SessionID != "" {
			s.remember(req.SessionID, req.Tool)
		}
		return nil
	default:
		return ErrDenied
	}
}

func (s *Service) currentPolicy() Policy {
	if s.policy == nil {
		return PolicyAsk
	}
	return ParsePolicy(string(s.policy()))
}

func allowKey(sessionID, tool string) string {
	return sessionID + "\x00" + tool
}

func (s *Service) remembered(sessionID, tool string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.allowed[allowKey(sessionID, tool)]
}

func (s *Service) remember(sessionID, tool string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.allowed[allowKey(sessionID, tool)] = true
}

// ForgetSession drops the session's allowlist and cancels any in-flight ask
// for it, so a closed SSH session cannot leave a dangling modal or a grant
// that would apply to a later connection that reused the id.
func (s *Service) ForgetSession(sessionID string) {
	if s == nil || sessionID == "" {
		return
	}
	s.mu.Lock()
	prefix := sessionID + "\x00"
	for k := range s.allowed {
		if strings.HasPrefix(k, prefix) {
			delete(s.allowed, k)
		}
	}
	gate := s.gate
	s.mu.Unlock()
	if c, ok := gate.(interface{ CancelSession(string) }); ok {
		c.CancelSession(sessionID)
	}
}

// DisposeAll drops every grant and cancels every pending ask (process
// shutdown).
func (s *Service) DisposeAll() {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.allowed = map[string]bool{}
	gate := s.gate
	s.mu.Unlock()
	if d, ok := gate.(interface{ DisposeAll() }); ok {
		d.DisposeAll()
	}
}

// Truncate bounds a command or path shown in a prompt. Contents never go
// through here; callers pass only the display line.
func Truncate(s string) string {
	const max = 160
	r := []rune(strings.TrimSpace(s))
	if len(r) <= max {
		return string(r)
	}
	return string(r[:max]) + "…"
}

// DenyGate always denies the current ask. Tests inject it so a denied tool
// can be shown not to run; production uses ChannelGate or NativeGate.
type DenyGate struct{}

func (DenyGate) Ask(context.Context, Request) (Decision, error) {
	return DecisionDeny, nil
}

// AllowGate always allows once. Tests inject it to prove the ask path still
// reaches execution.
type AllowGate struct{}

func (AllowGate) Ask(context.Context, Request) (Decision, error) {
	return DecisionAllowOnce, nil
}

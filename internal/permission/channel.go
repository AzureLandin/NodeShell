package permission

import (
	"context"
	"sync"

	"github.com/google/uuid"
)

type pendingAsk struct {
	sessionID string
	ch        chan Decision
}

// ChannelGate prompts through Wails events and unblocks when the renderer
// calls Decide. One Gate serves every session; pending asks are keyed by id
// so two tools cannot steal each other's answer.
type ChannelGate struct {
	emit   Emitter
	nextID func() string

	mu      sync.Mutex
	pending map[string]*pendingAsk
}

// NewChannelGate builds a Gate that emits EventAsk on emit. A nil emitter
// still waits for Decide, so a test can drive the channel without a UI.
func NewChannelGate(emit Emitter) *ChannelGate {
	return &ChannelGate{
		emit:    emit,
		nextID:  uuid.NewString,
		pending: map[string]*pendingAsk{},
	}
}

// Ask emits the prompt and blocks until Decide, CancelSession, DisposeAll or
// ctx cancellation. A cancelled ask emits EventClosed so the modal does not
// stick after the user hits stop.
func (g *ChannelGate) Ask(ctx context.Context, req Request) (Decision, error) {
	if err := ctx.Err(); err != nil {
		return DecisionDeny, err
	}
	id := g.nextID()
	req.ID = id
	ch := make(chan Decision, 1)
	g.mu.Lock()
	g.pending[id] = &pendingAsk{sessionID: req.SessionID, ch: ch}
	g.mu.Unlock()
	defer g.remove(id)

	if g.emit != nil {
		g.emit.Emit(EventAsk, req)
	}
	select {
	case d := <-ch:
		return d, nil
	case <-ctx.Done():
		if g.emit != nil {
			g.emit.Emit(EventClosed, ClosedEvent{ID: id})
		}
		return DecisionDeny, ctx.Err()
	}
}

// Decide unblocks the matching Ask. Unknown ids are ignored (already
// cancelled or answered).
func (g *ChannelGate) Decide(id string, d Decision) {
	if g == nil || id == "" {
		return
	}
	g.mu.Lock()
	p := g.pending[id]
	g.mu.Unlock()
	if p == nil {
		return
	}
	select {
	case p.ch <- d:
	default:
	}
}

// CancelSession denies every pending ask for the session and emits closed
// for each, so a disconnected host cannot leave a modal on screen.
func (g *ChannelGate) CancelSession(sessionID string) {
	if g == nil || sessionID == "" {
		return
	}
	ids := g.signalMatching(func(p *pendingAsk) bool { return p.sessionID == sessionID })
	g.emitClosed(ids)
}

// DisposeAll denies every pending ask (app shutdown).
func (g *ChannelGate) DisposeAll() {
	if g == nil {
		return
	}
	ids := g.signalMatching(func(*pendingAsk) bool { return true })
	g.emitClosed(ids)
}

func (g *ChannelGate) signalMatching(match func(*pendingAsk) bool) []string {
	g.mu.Lock()
	defer g.mu.Unlock()
	var ids []string
	for id, p := range g.pending {
		if !match(p) {
			continue
		}
		select {
		case p.ch <- DecisionDeny:
		default:
		}
		ids = append(ids, id)
	}
	return ids
}

func (g *ChannelGate) emitClosed(ids []string) {
	if g.emit == nil {
		return
	}
	for _, id := range ids {
		g.emit.Emit(EventClosed, ClosedEvent{ID: id})
	}
}

func (g *ChannelGate) remove(id string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	delete(g.pending, id)
}

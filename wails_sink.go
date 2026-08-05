package main

import (
	"context"

	"github.com/wailsapp/wails/v2/pkg/runtime"
)

// wailsSink adapts the sessions.EventSink to the Wails runtime: every
// session:data/closed/error event is emitted through runtime.EventsEmit, which
// the frontend adapter subscribes to via runtime.EventsOn. Emit is safe from
// any goroutine. Only nil-safety is claimed here: a nil context (unit tests,
// an App that never reached OnStartup) drops the event, and so does a plain
// (non-Wails) context — runtime.EventsEmit fatal-exits when the context does
// not carry the Wails runtime, so the value check mirrors the one runtime
// itself performs; a cancelled context is not special-cased.
type wailsSink struct {
	ctx context.Context
}

func (s *wailsSink) Emit(event string, payload any) {
	if s.ctx == nil || s.ctx.Value("events") == nil {
		return
	}
	runtime.EventsEmit(s.ctx, event, payload)
}

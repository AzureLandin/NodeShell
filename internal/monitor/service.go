package monitor

import (
	"context"
	"errors"
	"math"
	"sync"
	"time"
)

// Event names match src/shared/types.ts IPC constants.
const (
	EventUpdate = "monitor:update"
)

// Polling defaults (Electron parity); they are unexported after construction
// — tests inject their own via Deps, production always uses these.
const (
	DefaultInterval    = 4 * time.Second
	DefaultExecTimeout = 12 * time.Second
)

// Execer runs one remote sampling command over a session (the production
// implementation is *sessions.Manager; tests inject a fake). ctx cancellation
// must abort the command; timeout bounds it.
type Execer interface {
	Exec(sessionID string, ctx context.Context, command string, timeout time.Duration) (string, error)
}

// EventSink is the monitor:update emission seam; a nil sink is a no-op.
// Emit must not synchronously reenter the Service lifecycle (SetActive,
// Dispose, DisposeAll): the clear path emits while holding the transition
// lock, so a reentrant sink would deadlock.
type EventSink interface {
	Emit(event string, payload any)
}

type nopSink struct{}

func (nopSink) Emit(string, any) {}

// Process matches MonitorProcess in the frontend.
type Process = MonitorProcess

// Snapshot is one MonitorSnapshot; JSON field names match the frontend DTO
// exactly. cpuPercent/netRxBps/netTxBps are pointers so the first sample
// serialises as null.
type Snapshot struct {
	Title          string    `json:"title"`
	CPUPercent     *float64  `json:"cpuPercent"`
	MemUsedBytes   int64     `json:"memUsedBytes"`
	MemTotalBytes  int64     `json:"memTotalBytes"`
	SwapUsedBytes  int64     `json:"swapUsedBytes"`
	SwapTotalBytes int64     `json:"swapTotalBytes"`
	Load1          float64   `json:"load1"`
	Load5          float64   `json:"load5"`
	Load15         float64   `json:"load15"`
	NetRxBps       *float64  `json:"netRxBps"`
	NetTxBps       *float64  `json:"netTxBps"`
	Processes      []Process `json:"processes"`
	UpdatedAt      int64     `json:"updatedAt"`
}

// UpdateEvent is one monitor:update payload (matches MonitorUpdateEvent).
// sessionId and snapshot are pointers so clear/error events serialise the
// JSON null the frontend expects.
type UpdateEvent struct {
	SessionID *string   `json:"sessionId"`
	Snapshot  *Snapshot `json:"snapshot"`
	Error     string    `json:"error,omitempty"`
}

// Deps wires a Service. Interval/ExecTimeout default to the production
// constants when zero; tests inject shorter ones.
type Deps struct {
	Execer      Execer
	Sink        EventSink
	Interval    time.Duration
	ExecTimeout time.Duration
}

// Service polls exactly one active session every 4s. Only the current
// generation of poller may emit: switching or disposing cancels and joins the
// previous poller before the next one starts, so no stale event can cross a
// switch. Exec failures surface as update events (snapshot null, generic
// message) and never tear down the session.
type Service struct {
	execer      Execer
	sink        EventSink
	interval    time.Duration
	execTimeout time.Duration

	// mu guards active/closed only; it is never held while a poller is being
	// cancelled/joined or while an event is emitted.
	mu sync.Mutex
	// trans serialises the full transition of SetActive/Dispose/DisposeAll
	// (cancel → join → publish/start), so a concurrent switch can never
	// observe the active pointer in the detached state and publish over an
	// unjoined generation.
	trans  sync.Mutex
	active *poller
	closed bool
}

// New builds a Service.
func New(d Deps) *Service {
	if d.Sink == nil {
		d.Sink = nopSink{}
	}
	if d.Interval <= 0 {
		d.Interval = DefaultInterval
	}
	if d.ExecTimeout <= 0 {
		d.ExecTimeout = DefaultExecTimeout
	}
	return &Service{execer: d.Execer, sink: d.Sink, interval: d.Interval, execTimeout: d.ExecTimeout}
}

// poller is one polling generation: its own context, its own prev sample, and
// its own goroutine. prev is touched only by the poll goroutine, so each
// generation starts from a null first sample. start is closed by the
// SetActive that published this generation once its initial null snapshot has
// been emitted, so the first data tick can never overtake it.
type poller struct {
	svc       *Service
	sessionID string
	title     string
	ctx       context.Context
	cancel    context.CancelFunc
	done      chan struct{}
	start     chan struct{}
	prev      *sampleState
}

type sampleState struct {
	cpu CpuCounters
	net NetCounters
	at  time.Time
}

// SetActive switches monitoring to sessionID (empty clears). The transition
// lock serialises the full switch — the previous poller is cancelled and
// joined before the next one is published, so an old poll's result can never
// arrive after the new generation's first event and two concurrent switches
// can never orphan a generation. The clear event is emitted while still
// holding the transition lock, so a concurrent switch or DisposeAll waits for
// the clear to be fully delivered before it starts — a stale clear can never
// cross into the next generation's timeline. A non-empty session emits an
// immediate null snapshot (after the lock is released, so a re-entrant sink
// cannot deadlock), then ticks once immediately, then every interval.
func (s *Service) SetActive(sessionID, title string) {
	s.trans.Lock()
	if !s.beginSwitch() {
		s.trans.Unlock()
		return // shutdown already ran; never publish during teardown
	}
	if sessionID == "" {
		s.sink.Emit(EventUpdate, UpdateEvent{})
		s.trans.Unlock()
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	p := &poller{
		svc:       s,
		sessionID: sessionID,
		title:     title,
		ctx:       ctx,
		cancel:    cancel,
		done:      make(chan struct{}),
		start:     make(chan struct{}),
	}
	s.mu.Lock()
	s.active = p
	s.mu.Unlock()
	// Start the poller parked on its gate, then hand the lock over: the first
	// event (null snapshot) is emitted after the lock is released so a sink
	// that re-enters SetActive/Dispose can never deadlock, and the gate is
	// opened unconditionally afterwards so a concurrent switch that already
	// cancelled this generation still joins it.
	go p.run()
	s.trans.Unlock()
	s.sink.Emit(EventUpdate, UpdateEvent{SessionID: &sessionID})
	close(p.start)
}

// Dispose stops monitoring only when sessionID is the active session (the
// session:closed hook). Non-matching ids are a no-op.
func (s *Service) Dispose(sessionID string) {
	s.trans.Lock()
	defer s.trans.Unlock()
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	p := s.active
	if p == nil || p.sessionID != sessionID {
		s.mu.Unlock()
		return
	}
	s.active = nil
	s.mu.Unlock()
	p.cancel()
	<-p.done
}

// DisposeAll stops and joins the active poller and blocks any later SetActive
// (app shutdown); nothing is emitted.
func (s *Service) DisposeAll() {
	s.trans.Lock()
	defer s.trans.Unlock()
	s.mu.Lock()
	s.closed = true
	p := s.active
	s.active = nil
	s.mu.Unlock()
	if p != nil {
		p.cancel()
		<-p.done
	}
}

// beginSwitch detaches the current poller (cancelling and joining it) and
// reports whether the service still accepts a new generation.
func (s *Service) beginSwitch() bool {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return false
	}
	old := s.active
	s.active = nil
	s.mu.Unlock()
	if old != nil {
		old.cancel()
		<-old.done
	}
	return true
}

// isCurrent reports whether p is still the active poller; the poll goroutine
// checks it before every emit so a cancelled generation stays silent.
func (s *Service) isCurrent(p *poller) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.active == p
}

// run is the poller goroutine: it waits for its start gate (the initial null
// snapshot must precede the first tick), does one immediate tick, then
// interval ticks. The synchronous tick never overlaps itself (a slow exec
// simply delays the next tick), and cancellation exits the loop promptly —
// including a cancellation that landed before the gate was opened.
func (p *poller) run() {
	defer close(p.done)
	<-p.start
	if p.ctx.Err() != nil {
		return
	}
	p.tick()
	t := time.NewTicker(p.svc.interval)
	defer t.Stop()
	for {
		select {
		case <-t.C:
			p.tick()
		case <-p.ctx.Done():
			return
		}
	}
}

func (p *poller) tick() {
	if !p.svc.isCurrent(p) {
		return
	}
	if p.svc.execer == nil {
		// A mis-wired service must fail observably — never panic inside the
		// poll goroutine, which would skip close(done) and hang every join.
		p.fail(errors.New("Monitor unavailable"))
		return
	}
	out, err := p.svc.execer.Exec(p.sessionID, p.ctx, Script(), p.svc.execTimeout)
	if err != nil {
		p.fail(err)
		return
	}
	raw, err := parseMonitorOutput(out)
	if err != nil {
		p.failTrusted(err)
		return
	}
	now := time.Now()
	var cpuPercent *float64
	var netRxBps, netTxBps *float64
	if p.prev != nil {
		c := cpuPercentFromDelta(p.prev.cpu, raw.CPU)
		cpuPercent = &c
		if dt := now.Sub(p.prev.at).Seconds(); dt > 0 {
			rx := math.Max(0, float64(raw.Net.Rx-p.prev.net.Rx)/dt)
			tx := math.Max(0, float64(raw.Net.Tx-p.prev.net.Tx)/dt)
			netRxBps = &rx
			netTxBps = &tx
		}
	}
	p.prev = &sampleState{cpu: raw.CPU, net: raw.Net, at: now}
	if !p.svc.isCurrent(p) {
		return
	}
	snap := Snapshot{
		Title:          p.title,
		CPUPercent:     cpuPercent,
		MemUsedBytes:   kbToBytes(max64(0, raw.MemTotalKb-raw.MemAvailableKb)),
		MemTotalBytes:  kbToBytes(raw.MemTotalKb),
		SwapUsedBytes:  kbToBytes(max64(0, raw.SwapTotalKb-raw.SwapFreeKb)),
		SwapTotalBytes: kbToBytes(raw.SwapTotalKb),
		Load1:          raw.Load1,
		Load5:          raw.Load5,
		Load15:         raw.Load15,
		NetRxBps:       netRxBps,
		NetTxBps:       netTxBps,
		Processes:      raw.Processes,
		UpdatedAt:      now.UnixMilli(),
	}
	p.svc.sink.Emit(EventUpdate, UpdateEvent{SessionID: &p.sessionID, Snapshot: &snap})
}

// codedError is implemented by errors that carry a stable machine-readable
// code (the production sessions/sshclient errors); only their messages — which
// are hand-written generic strings that never embed paths or secrets — are
// trusted for the frontend.
type codedError interface {
	ErrorCode() string
}

// fail emits a snapshot-null error event when this poller is still current.
// The message stays generic: a plain execer error collapses to the fixed
// "Monitor unavailable" so an arbitrary err.Error() can never leak an Execer
// path or secret to the frontend; only a coded error's stable message passes
// through. The session itself is never closed by a monitoring failure.
func (p *poller) fail(err error) {
	msg := "Monitor unavailable"
	var coded codedError
	if errors.As(err, &coded) {
		if m := err.Error(); m != "" {
			msg = m
		}
	}
	p.emitError(msg)
}

// failTrusted emits a snapshot-null error event when this poller is still
// current. It is used only for parser errors whose messages are fixed strings
// owned by this package (never derived from remote output), so the text is
// safe to surface verbatim.
func (p *poller) failTrusted(err error) {
	msg := err.Error()
	if msg == "" {
		msg = "Monitor unavailable"
	}
	p.emitError(msg)
}

func (p *poller) emitError(msg string) {
	if !p.svc.isCurrent(p) {
		return
	}
	p.svc.sink.Emit(EventUpdate, UpdateEvent{SessionID: &p.sessionID, Error: msg})
}

func max64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}

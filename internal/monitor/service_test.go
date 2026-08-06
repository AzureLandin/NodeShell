package monitor

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"
)

// sampleOutput2 advances the /proc counters so a second poll yields deltas:
// cpu idle 85→115 (+30), total 100→200 (+100) ⇒ 70%; net rx 100→5000.
const sampleOutput2 = `---STAT---
cpu  10 0 75 115 0 0 0 0
---MEM---
MemTotal:        1000000 kB
MemAvailable:     400000 kB
SwapTotal:             0 kB
SwapFree:              0 kB
---LOAD---
1.50 2.50 3.50 1/1 1
---NET---
Inter-|   Receive                                                |  Transmit
 face |bytes    packets errs drop fifo frame compressed multicast|bytes    packets errs drop fifo colls carrier compressed
  eth0: 5000 0 0 0 0 0 0 0 8000 0 0 0 0 0 0 0
---PS---
91234  1.7 YDService
`

// recordSink records every monitor:update payload.
type recordSink struct {
	mu     sync.Mutex
	events []UpdateEvent
}

func (s *recordSink) Emit(event string, payload any) {
	if event != EventUpdate {
		return
	}
	ev, ok := payload.(UpdateEvent)
	if !ok {
		return
	}
	s.mu.Lock()
	s.events = append(s.events, ev)
	s.mu.Unlock()
}

func (s *recordSink) all() []UpdateEvent {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]UpdateEvent(nil), s.events...)
}

func (s *recordSink) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.events)
}

// waitEvents polls the sink until at least n events landed.
func waitEvents(t *testing.T, s *recordSink, n int) []UpdateEvent {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		if ev := s.all(); len(ev) >= n {
			return ev
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %d events, have %d", n, s.count())
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// waitTrue polls cond until it holds or the deadline passes.
func waitTrue(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		if cond() {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("condition never became true: %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// staticExecer returns outputs[i] for the i-th call (last repeats).
type staticExecer struct {
	mu      sync.Mutex
	outputs []string
	timeout time.Duration
	calls   int
}

func (e *staticExecer) Exec(_ string, _ context.Context, _ string, timeout time.Duration) (string, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.calls++
	e.timeout = timeout
	i := e.calls - 1
	if i >= len(e.outputs) {
		i = len(e.outputs) - 1
	}
	return e.outputs[i], nil
}

func (e *staticExecer) lastTimeout() time.Duration {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.timeout
}

func (e *staticExecer) callCount() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.calls
}

// blockingExecer blocks each exec until release is closed (or ctx dies) and
// tracks concurrency.
type blockingExecer struct {
	landed    chan struct{}
	release   chan struct{}
	landOnce  sync.Once
	mu        sync.Mutex
	active    int
	maxActive int
}

func newBlockingExecer() *blockingExecer {
	return &blockingExecer{landed: make(chan struct{}), release: make(chan struct{})}
}

func (b *blockingExecer) Exec(_ string, ctx context.Context, _ string, _ time.Duration) (string, error) {
	b.landOnce.Do(func() { close(b.landed) })
	b.mu.Lock()
	b.active++
	if b.active > b.maxActive {
		b.maxActive = b.active
	}
	b.mu.Unlock()
	defer func() {
		b.mu.Lock()
		b.active--
		b.mu.Unlock()
	}()
	select {
	case <-ctx.Done():
		return "", errors.New("cancelled")
	case <-b.release:
		return sampleOutput, nil
	}
}

func (b *blockingExecer) concurrency() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.maxActive
}

// gateExecer blocks every exec until its ctx is cancelled, then records the
// exit — so started must equal exited after a clean shutdown, and any poller
// the service forgot to cancel shows up as a positive balance. The FIRST exec
// additionally signals the test and parks on a release gate: the switch that
// detaches that generation is therefore held inside its join (with active
// already nil), which deterministically exposes the old implementation's
// beginSwitch→publish gap to a concurrent second SetActive.
type gateExecer struct {
	mu           sync.Mutex
	started      int
	exited       int
	firstExiting chan struct{}
	releaseFirst chan struct{}
}

func newGateExecer() *gateExecer {
	return &gateExecer{firstExiting: make(chan struct{}), releaseFirst: make(chan struct{})}
}

func (g *gateExecer) Exec(_ string, ctx context.Context, _ string, _ time.Duration) (string, error) {
	g.mu.Lock()
	g.started++
	first := g.started == 1
	g.mu.Unlock()
	<-ctx.Done()
	if first {
		close(g.firstExiting)
		<-g.releaseFirst
	}
	g.mu.Lock()
	g.exited++
	g.mu.Unlock()
	return "", ctx.Err()
}

func (g *gateExecer) counts() (started, exited int) {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.started, g.exited
}

// clearBlockingSink records non-clear events immediately but holds the clear
// event until releaseClear closes. Because the clear is only recorded AFTER it
// is released, the recorded order exposes whether a concurrent switch's events
// overtook an in-flight clear emit: a stale clear lands last.
type clearBlockingSink struct {
	mu           sync.Mutex
	events       []UpdateEvent
	clearLanded  chan struct{}
	releaseClear chan struct{}
	clearOnce    sync.Once
}

func newClearBlockingSink() *clearBlockingSink {
	return &clearBlockingSink{clearLanded: make(chan struct{}), releaseClear: make(chan struct{})}
}

func (s *clearBlockingSink) Emit(event string, payload any) {
	if event != EventUpdate {
		return
	}
	ev, ok := payload.(UpdateEvent)
	if !ok {
		return
	}
	if ev.SessionID == nil {
		s.clearOnce.Do(func() { close(s.clearLanded) })
		<-s.releaseClear
	}
	s.mu.Lock()
	s.events = append(s.events, ev)
	s.mu.Unlock()
}

func (s *clearBlockingSink) all() []UpdateEvent {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]UpdateEvent(nil), s.events...)
}

func (s *clearBlockingSink) hasSessionEvents() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, ev := range s.events {
		if ev.SessionID != nil {
			return true
		}
	}
	return false
}

// errExecer fails every exec with the given error.
type errExecer struct{ err error }

func (e *errExecer) Exec(_ string, _ context.Context, _ string, _ time.Duration) (string, error) {
	return "", e.err
}

func newTestService(t *testing.T, execer Execer, sink EventSink, interval time.Duration) *Service {
	t.Helper()
	return New(Deps{Execer: execer, Sink: sink, Interval: interval})
}

// TestDTOJSONNullSemantics: the emitted DTO serialises exactly like
// MonitorUpdateEvent / MonitorSnapshot — null sessionId/snapshot on clear,
// null cpuPercent/netRxBps/netTxBps on the first sample.
func TestDTOJSONNullSemantics(t *testing.T) {
	// clear event
	clearJSON, err := json.Marshal(UpdateEvent{})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if string(clearJSON) != `{"sessionId":null,"snapshot":null}` {
		t.Fatalf("clear event JSON = %s", clearJSON)
	}
	// first sample: null metrics, non-nil snapshot
	sid := "s1"
	first := UpdateEvent{SessionID: &sid, Snapshot: &Snapshot{Title: "t", Processes: []MonitorProcess{}}}
	raw, err := json.Marshal(first)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	text := string(raw)
	for _, field := range []string{`"sessionId":"s1"`, `"title":"t"`, `"cpuPercent":null`, `"netRxBps":null`, `"netTxBps":null`, `"updatedAt":0`, `"processes":[]`} {
		if !strings.Contains(text, field) {
			t.Fatalf("snapshot JSON %s missing %s", text, field)
		}
	}
	// a populated delta snapshot
	v := 42.5
	full := UpdateEvent{SessionID: &sid, Snapshot: &Snapshot{
		Title: "t", CPUPercent: &v, MemUsedBytes: 1, MemTotalBytes: 2,
		SwapUsedBytes: 3, SwapTotalBytes: 4, Load1: 0.1, Load5: 0.2, Load15: 0.3,
		NetRxBps: &v, NetTxBps: &v, Processes: []MonitorProcess{{MemBytes: 9, CPUPercent: 1, Command: "c"}}, UpdatedAt: 5,
	}}
	raw, err = json.Marshal(full)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	snap := decoded["snapshot"].(map[string]any)
	if snap["cpuPercent"].(float64) != 42.5 {
		t.Fatalf("cpuPercent = %v", snap["cpuPercent"])
	}
	procs := snap["processes"].([]any)
	proc := procs[0].(map[string]any)
	if proc["memBytes"].(float64) != 9 || proc["command"].(string) != "c" {
		t.Fatalf("process = %v", proc)
	}
}

// TestClearEmitPrecedesConcurrentSwitch pins the clear-ordering invariant: a
// SetActive("") clear must be fully delivered before a concurrent switch's
// events, so no stale clear can cross into the new session's timeline. A's
// clear emit is parked inside the sink (on the old implementation A had
// already released the transition lock, so B's switch runs to completion while
// the clear is still in flight; on the fixed one B blocks on the lock A holds
// during the clear emit). The sink only records the clear after it is
// released, so the recorded order deterministically exposes the crossing.
func TestClearEmitPrecedesConcurrentSwitch(t *testing.T) {
	sink := newClearBlockingSink()
	execer := &staticExecer{outputs: []string{sampleOutput}}
	svc := newTestService(t, execer, sink, time.Hour)

	var wg sync.WaitGroup
	wg.Add(1)
	go func() { defer wg.Done(); svc.SetActive("", "") }() // A: clear
	select {
	case <-sink.clearLanded:
	case <-time.After(5 * time.Second):
		t.Fatal("timeout waiting for clear emit")
	}

	// B switches to s2 while the clear emit has not returned.
	wg.Add(1)
	go func() { defer wg.Done(); svc.SetActive("s2", "t") }() // B
	// Bounded wait for the old-only crossing; on the fixed implementation B
	// stays blocked behind the clear and the grace simply elapses.
	deadline := time.Now().Add(300 * time.Millisecond)
	for {
		if sink.hasSessionEvents() {
			break
		}
		if time.Now().After(deadline) {
			break
		}
		time.Sleep(2 * time.Millisecond)
	}
	close(sink.releaseClear)
	wg.Wait()

	evs := sink.all()
	if len(evs) == 0 {
		t.Fatal("no clear event was emitted")
	}
	if evs[0].SessionID != nil {
		t.Fatalf("stale clear: %+v was delivered after the s2 switch events; a clear must precede the switch it ends (all: %+v)", evs[0], evs)
	}
	svc.DisposeAll()
}

// TestDisposeAllWaitsForClearEmit pins that DisposeAll cannot return before an
// in-flight clear emit completes: the clear must be delivered before shutdown
// starts tearing the app down, so no monitor:update can be dropped mid-tear.
func TestDisposeAllWaitsForClearEmit(t *testing.T) {
	sink := newClearBlockingSink()
	svc := newTestService(t, &staticExecer{outputs: []string{sampleOutput}}, sink, time.Hour)

	var wg sync.WaitGroup
	wg.Add(1)
	go func() { defer wg.Done(); svc.SetActive("", "") }() // clear
	select {
	case <-sink.clearLanded:
	case <-time.After(5 * time.Second):
		t.Fatal("timeout waiting for clear emit")
	}

	disposed := make(chan struct{})
	go func() { svc.DisposeAll(); close(disposed) }()

	select {
	case <-disposed:
		t.Fatal("DisposeAll returned before the in-flight clear emit completed")
	case <-time.After(150 * time.Millisecond):
		// correct: DisposeAll is serialised behind the clear emit
	}
	close(sink.releaseClear)
	wg.Wait()
	<-disposed
}

func TestSetActiveEmptyEmitsClearImmediately(t *testing.T) {
	sink := &recordSink{}
	execer := &staticExecer{outputs: []string{sampleOutput}}
	svc := newTestService(t, execer, sink, time.Hour)
	svc.SetActive("", "")
	events := waitEvents(t, sink, 1)
	ev := events[0]
	if ev.SessionID != nil || ev.Snapshot != nil || ev.Error != "" {
		t.Fatalf("clear event = %+v, want {nil sessionId, nil snapshot}", ev)
	}
	if got := execer.callCount(); got != 0 {
		t.Fatalf("clear must not exec, got %d execs", got)
	}
}

func TestSetActiveFirstTickNullMetrics(t *testing.T) {
	sink := &recordSink{}
	execer := &staticExecer{outputs: []string{sampleOutput}}
	svc := newTestService(t, execer, sink, time.Hour)
	svc.SetActive("s1", "my title")
	events := waitEvents(t, sink, 2)
	if events[0].SessionID == nil || *events[0].SessionID != "s1" || events[0].Snapshot != nil {
		t.Fatalf("first event = %+v, want {s1, null snapshot}", events[0])
	}
	snap := events[1].Snapshot
	if snap == nil {
		t.Fatal("second event must carry a snapshot")
	}
	if snap.Title != "my title" {
		t.Fatalf("title = %q", snap.Title)
	}
	if snap.CPUPercent != nil || snap.NetRxBps != nil || snap.NetTxBps != nil {
		t.Fatalf("first sample must have null metrics: %+v", snap)
	}
	if snap.MemUsedBytes != 600000*1024 || snap.MemTotalBytes != 1000000*1024 {
		t.Fatalf("mem = %d/%d", snap.MemUsedBytes, snap.MemTotalBytes)
	}
	if snap.Load1 != 1.0 || snap.Load5 != 2.0 || snap.Load15 != 3.0 {
		t.Fatalf("load = %v %v %v", snap.Load1, snap.Load5, snap.Load15)
	}
	if len(snap.Processes) != 3 || snap.Processes[0].Command != "YDService" {
		t.Fatalf("processes = %+v", snap.Processes)
	}
	if snap.UpdatedAt == 0 {
		t.Fatal("updatedAt must be set")
	}
	if execer.lastTimeout() != DefaultExecTimeout {
		t.Fatalf("exec timeout = %v, want DefaultExecTimeout", execer.lastTimeout())
	}
}

func TestSecondTickComputesDeltas(t *testing.T) {
	sink := &recordSink{}
	execer := &staticExecer{outputs: []string{sampleOutput, sampleOutput2}}
	svc := newTestService(t, execer, sink, 15*time.Millisecond)
	svc.SetActive("s1", "t")
	events := waitEvents(t, sink, 3)
	second := events[2].Snapshot
	if second == nil {
		t.Fatal("third event must carry a snapshot")
	}
	if second.CPUPercent == nil {
		t.Fatal("cpuPercent must be computed on the second sample")
	}
	if *second.CPUPercent < 69.9 || *second.CPUPercent > 70.1 {
		t.Fatalf("cpuPercent = %v, want ~70", *second.CPUPercent)
	}
	if second.NetRxBps == nil || second.NetTxBps == nil {
		t.Fatal("net deltas must be computed on the second sample")
	}
	if *second.NetRxBps < 0 || *second.NetTxBps < 0 {
		t.Fatalf("net deltas must be non-negative: %v/%v", *second.NetRxBps, *second.NetTxBps)
	}
	if second.UpdatedAt <= events[1].Snapshot.UpdatedAt {
		t.Fatal("second updatedAt must be newer")
	}
}

func TestSwitchCancelsOldPollerAndNoOldEvents(t *testing.T) {
	sink := &recordSink{}
	execer := newBlockingExecer()
	svc := newTestService(t, execer, sink, time.Hour)
	svc.SetActive("s1", "")
	select {
	case <-execer.landed:
	case <-time.After(5 * time.Second):
		t.Fatal("timeout waiting for poller exec")
	}
	// Switch away: must cancel + join the old poller before the new one runs.
	start := time.Now()
	svc.SetActive("s2", "")
	if time.Since(start) > 3*time.Second {
		t.Fatal("switch must cancel the old poller promptly")
	}
	// Releasing the old exec afterwards must not produce an s1 event: check
	// only events that arrived after the switch (the initial {s1,null} from
	// the first SetActive is legitimately before it).
	after := sink.all()
	close(execer.release)
	time.Sleep(50 * time.Millisecond)
	for _, ev := range sink.all()[len(after):] {
		if ev.SessionID != nil && *ev.SessionID == "s1" {
			t.Fatalf("old session event crossed the switch: %+v", ev)
		}
	}
}

func TestNoReentrantPoll(t *testing.T) {
	sink := &recordSink{}
	execer := newBlockingExecer()
	svc := newTestService(t, execer, sink, 5*time.Millisecond)
	svc.SetActive("s1", "")
	select {
	case <-execer.landed:
	case <-time.After(5 * time.Second):
		t.Fatal("timeout waiting for poller exec")
	}
	// Let many ticker fires accumulate while the exec is stuck; only one poll
	// may ever run at a time.
	time.Sleep(40 * time.Millisecond)
	if got := execer.concurrency(); got != 1 {
		t.Fatalf("max concurrent execs = %d, want 1", got)
	}
	close(execer.release)
	// After the first poll finishes, the next ticker fire may poll again, but
	// still never concurrently.
	waitTrue(t, "second poll lands", func() bool {
		s := sink.all()
		return len(s) >= 2
	})
	time.Sleep(20 * time.Millisecond)
	if got := execer.concurrency(); got != 1 {
		t.Fatalf("max concurrent execs = %d, want 1", got)
	}
}

func TestIntervalTicks(t *testing.T) {
	sink := &recordSink{}
	execer := &staticExecer{outputs: []string{sampleOutput, sampleOutput2}}
	svc := newTestService(t, execer, sink, 10*time.Millisecond)
	svc.SetActive("s1", "")
	waitEvents(t, sink, 4) // null + tick + tick + tick
	if n := execer.callCount(); n < 3 {
		t.Fatalf("execs = %d, want >= 3 (interval ticks)", n)
	}
	svc.Dispose("s1")
}

func TestDisposeStopsActivePoller(t *testing.T) {
	sink := &recordSink{}
	execer := &staticExecer{outputs: []string{sampleOutput, sampleOutput2}}
	svc := newTestService(t, execer, sink, 10*time.Millisecond)
	svc.SetActive("s1", "")
	waitEvents(t, sink, 2)
	svc.Dispose("s1")
	time.Sleep(30 * time.Millisecond)
	before := execer.callCount()
	time.Sleep(30 * time.Millisecond)
	if got := execer.callCount(); got != before {
		t.Fatalf("poller still execs after Dispose: %d -> %d", before, got)
	}
}

func TestDisposeNonMatchingSessionIsNoop(t *testing.T) {
	sink := &recordSink{}
	execer := &staticExecer{outputs: []string{sampleOutput, sampleOutput2}}
	svc := newTestService(t, execer, sink, 10*time.Millisecond)
	svc.SetActive("s1", "")
	svc.Dispose("other")
	waitEvents(t, sink, 3) // still polling after the non-matching dispose
	svc.Dispose("s1")
}

func TestDisposeAllJoinsAndBlocksLaterSetActive(t *testing.T) {
	sink := &recordSink{}
	execer := newBlockingExecer()
	svc := newTestService(t, execer, sink, 5*time.Millisecond)
	svc.SetActive("s1", "")
	select {
	case <-execer.landed:
	case <-time.After(5 * time.Second):
		t.Fatal("timeout waiting for poller exec")
	}
	start := time.Now()
	svc.DisposeAll()
	if time.Since(start) > 3*time.Second {
		t.Fatal("DisposeAll must cancel + join the in-flight poll")
	}
	before := sink.count()
	// After DisposeAll no events may be emitted and no new poller may start.
	close(execer.release)
	time.Sleep(50 * time.Millisecond)
	if got := sink.count(); got != before {
		t.Fatalf("events after DisposeAll: %d -> %d", before, got)
	}
	svc.SetActive("s2", "")
	time.Sleep(30 * time.Millisecond)
	if got := sink.count(); got != before {
		t.Fatalf("SetActive after DisposeAll must be a no-op, events %d -> %d", before, got)
	}
}

func TestExecErrorEmitsGenericErrorEvent(t *testing.T) {
	sink := &recordSink{}
	svc := newTestService(t, &errExecer{err: errors.New("boom: /var/tmp/secret")}, sink, 15*time.Millisecond)
	svc.SetActive("s1", "")
	events := waitEvents(t, sink, 2)
	if events[0].Snapshot != nil {
		t.Fatalf("first event = %+v", events[0])
	}
	ev := events[1]
	if ev.SessionID == nil || *ev.SessionID != "s1" || ev.Snapshot != nil {
		t.Fatalf("error event = %+v", ev)
	}
	// A plain execer error must collapse to the fixed generic message: the raw
	// err.Error() may embed an Execer path or secret.
	if ev.Error != "Monitor unavailable" {
		t.Fatalf("error = %q, want the fixed generic message", ev.Error)
	}
	if strings.Contains(ev.Error, "/var/tmp") || strings.Contains(ev.Error, "secret") || strings.Contains(ev.Error, "boom") {
		t.Fatalf("error leaks the execer error text: %q", ev.Error)
	}
	// The session stays open: the service keeps polling (more error events).
	waitEvents(t, sink, 3)
}

// codedErr is a minimal coded error like the production sessions/sshclient
// errors, whose messages are hand-written generic strings safe to surface.
type codedErr struct{ message string }

func (e *codedErr) Error() string     { return e.message }
func (e *codedErr) ErrorCode() string { return "CONNECTION_REFUSED" }

func TestCodedExecErrorMessagePassesThrough(t *testing.T) {
	sink := &recordSink{}
	svc := newTestService(t, &errExecer{err: &codedErr{message: "Connection refused"}}, sink, time.Hour)
	svc.SetActive("s1", "")
	events := waitEvents(t, sink, 2)
	ev := events[1]
	if ev.Error != "Connection refused" {
		t.Fatalf("coded exec error = %q, want the stable message passed through", ev.Error)
	}
}

func TestNilExecerEmitsMonitorUnavailable(t *testing.T) {
	sink := &recordSink{}
	svc := New(Deps{Sink: sink, Interval: time.Hour}) // execer left nil
	svc.SetActive("s1", "")
	events := waitEvents(t, sink, 2)
	if events[1].Error != "Monitor unavailable" {
		t.Fatalf("nil execer error = %q, want the fixed message", events[1].Error)
	}
}

func TestParseErrorEmitsGenericErrorEvent(t *testing.T) {
	sink := &recordSink{}
	execer := &staticExecer{outputs: []string{"garbage without sections"}}
	svc := newTestService(t, execer, sink, time.Hour)
	svc.SetActive("s1", "")
	events := waitEvents(t, sink, 2)
	ev := events[1]
	if ev.Snapshot != nil {
		t.Fatalf("error event must have nil snapshot: %+v", ev)
	}
	if ev.Error != "Incomplete monitor output" {
		t.Fatalf("error = %q, want the generic parse error", ev.Error)
	}
	if strings.ContainsAny(ev.Error, `/\`) {
		t.Fatalf("parse error must be path-free: %q", ev.Error)
	}
}

func TestExecTimeoutFixedAt12s(t *testing.T) {
	sink := &recordSink{}
	execer := &staticExecer{outputs: []string{sampleOutput}}
	// Production defaults apply when nothing is injected.
	svc := New(Deps{Execer: execer, Sink: sink})
	if svc.interval != DefaultInterval {
		t.Fatalf("default interval = %v, want %v", svc.interval, DefaultInterval)
	}
	if svc.execTimeout != DefaultExecTimeout {
		t.Fatalf("default exec timeout = %v, want %v", svc.execTimeout, DefaultExecTimeout)
	}
	svc.SetActive("s1", "")
	waitEvents(t, sink, 2)
	if got := execer.lastTimeout(); got != DefaultExecTimeout {
		t.Fatalf("Exec timeout = %v, want 12s", got)
	}
	svc.Dispose("s1")
}

// TestConcurrentSwitchStress hammers SetActive (including clears) from many
// goroutines while interval ticks fire: the service must never race (checked
// by the -race detector), leave the active pointer on the wrong poller, or
// deadlock on the joins.
func TestConcurrentSwitchStress(t *testing.T) {
	sink := &recordSink{}
	execer := &staticExecer{outputs: []string{sampleOutput, sampleOutput2}}
	svc := newTestService(t, execer, sink, 2*time.Millisecond)
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			for j := 0; j < 40; j++ {
				if j%5 == 0 {
					svc.SetActive("", "")
				} else {
					svc.SetActive(fmt.Sprintf("s%d", n), "t")
				}
				time.Sleep(time.Duration(j%3) * time.Millisecond)
			}
		}(i)
	}
	wg.Wait()
	svc.DisposeAll()
	// After DisposeAll nothing may emit and no poller may run.
	before := sink.count()
	time.Sleep(30 * time.Millisecond)
	if got := sink.count(); got != before {
		t.Fatalf("events after DisposeAll: %d -> %d", before, got)
	}
}

// TestConcurrentSetActiveNeverOrphansPoller is the deterministic regression
// test for the publish-after-beginSwitch gap: two concurrent SetActive calls
// can both pass beginSwitch while active is nil, and the second publish then
// overwrites the first poller without ever cancelling or joining it — leaking
// a ticker/exec goroutine forever. The gateExecer barrier parks switch A
// inside its join of the baseline generation (active already nil), so switch
// B is guaranteed to publish into the gap on the old implementation. After
// DisposeAll, every started exec must have observed ctx cancellation.
func TestConcurrentSetActiveNeverOrphansPoller(t *testing.T) {
	sink := &recordSink{}
	execer := newGateExecer()
	svc := newTestService(t, execer, sink, time.Hour)

	// Baseline generation P1, parked inside its first exec.
	svc.SetActive("s1", "t1")
	waitTrue(t, "baseline exec lands", func() bool {
		s, _ := execer.counts()
		return s >= 1
	})

	// Switch A moves to s2: it detaches P1, then parks in the join (exec #1's
	// return is gated by the test), leaving active == nil.
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); svc.SetActive("s2", "t2") }() // A
	select {
	case <-execer.firstExiting:
	case <-time.After(5 * time.Second):
		t.Fatal("timeout waiting for first exiting")
	}

	// Switch B moves to s3 while the gap is open. On the old implementation B
	// passes beginSwitch (active == nil) and publishes P3; on the fixed one B
	// is serialised behind A's transition. The grace only waits out the
	// old-only publish signal — on the fixed implementation B stays blocked
	// and the grace simply elapses.
	go func() { defer wg.Done(); svc.SetActive("s3", "t3") }() // B
	deadline := time.Now().Add(300 * time.Millisecond)
	for {
		if s, _ := execer.counts(); s >= 2 {
			break
		}
		if time.Now().After(deadline) {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	close(execer.releaseFirst) // A joins P1, then publishes P2 over P3 on the old impl

	wg.Wait()
	svc.DisposeAll()

	// Every exec that started must have exited via ctx cancellation; a leaked
	// poller leaves a positive balance.
	started, exited := execer.counts()
	if active := started - exited; active != 0 {
		t.Fatalf("%d of %d started exec(s) never saw cancellation: a concurrent switch orphaned its poller", active, started)
	}
}

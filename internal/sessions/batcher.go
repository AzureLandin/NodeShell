package sessions

import (
	"fmt"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"
)

// Output batching constants mirror the Electron build's DATA_FLUSH_MS and
// DATA_FLUSH_BYTES. Adaptive coalescing may raise the flush *trigger*
// toward maxFlushBytes under backpressure, but never changes the 12ms
// interactive timer. Independently, every queued batch is hard-capped at
// maxFlushBytes: takeLocked splits pending on a UTF-8 boundary so a single
// huge SSH read cannot enqueue an oversized chunk.
const (
	flushInterval     = 12 * time.Millisecond
	flushBytes        = 48 * 1024
	maxFlushBytes     = 96 * 1024
	maxQueueBatches   = 64 // bounded backpressure queue (~3MB max per session at 48KiB)
	adaptiveQueueHigh = 8  // start raising the coalesce threshold at this queue depth
)

// outputBatcher coalesces PTY output into bounded batches: it emits at most
// every flushInterval, or as soon as the (possibly adaptive) byte threshold
// has accumulated, and holds back a trailing partial UTF-8 sequence so a
// rune split across SSH reads is never emitted as U+FFFD. Every emitted
// chunk is valid UTF-8 — genuinely invalid bytes (and an incomplete rune at
// Close) are replaced with U+FFFD.
//
// Concurrency & Teardown Architecture:
//   - A dedicated worker goroutine (run) consumes batches from a FIFO queue and delivers
//     them to the sink outside any lock, eliminating backpressure on mu.
//   - The queue has a maximum capacity (maxQueueBatches). When exceeded, Add blocks on cond,
//     exerting natural backpressure on SSH output pumps to prevent unbounded memory growth.
//   - Close() flushes remaining pending bytes, marks closed=true, broadcasts cond, and waits on done,
//     guaranteeing all pending batches are delivered before any Close() call returns.
//   - Discard() marks discarded=true, drops remaining queue/pending, broadcasts cond, and waits on done,
//     guaranteeing zero late emissions after Discard() returns.
//   - Concurrent Close() and Discard() calls always wait on done so every caller returns
//     only when teardown is completely finished.
//
// Sink contract (unsupported re-entry):
//
//	The sink runs without b.mu held, but it still runs on the worker goroutine.
//	A sink MUST NOT synchronously call Add, Close, or Discard on the same
//	batcher. Re-entrant Add can wait for queue capacity while the worker is
//	blocked inside the sink (deadlock). Re-entrant Close/Discard wait on done,
//	which the worker only closes after the sink returns (deadlock). Production
//	Wails EventsEmit is one-way and never re-enters the batcher. Tests that
//	need to observe the sink must use a separate goroutine to touch the
//	batcher. There is no second unbounded dispatch queue: that would either
//	drop output or leak a goroutine if the sink blocked forever.
type outputBatcher struct {
	mu           sync.Mutex
	cond         *sync.Cond
	pending      []byte
	queue        [][]byte
	timer        *time.Timer
	timerSet     bool
	closed       bool
	discarded    bool
	interval     time.Duration
	threshold    int
	thresholdCap int
	maxBatch     int // hard cap on one queued chunk; 0 means maxFlushBytes
	maxBatches   int
	adaptive     bool
	emit         func(data []byte)
	done         chan struct{}
	stats        batcherStats
}

// batcherStats is cheap, always-on accounting. Values never include payload
// bytes as strings, paths, or secrets — only counters. Logged only when
// NODESHELL_TERMINAL_METRICS=1, and then only as a single line at teardown.
type batcherStats struct {
	recvBytes      atomic.Int64
	emitBatches    atomic.Int64
	emitBytes      atomic.Int64
	queueLen       atomic.Int64
	queuePeak      atomic.Int64
	queueWaitCount atomic.Int64
	queueWaitNs    atomic.Int64
	sinkCalls      atomic.Int64
	sinkNs         atomic.Int64
}

// batcherSnapshot is the test/diagnostics view of batcherStats plus teardown
// flags. It is not a public API.
type batcherSnapshot struct {
	RecvBytes      int64
	EmitBatches    int64
	EmitBytes      int64
	QueueLen       int64
	QueuePeak      int64
	QueueWaitCount int64
	QueueWaitNs    int64
	SinkCalls      int64
	SinkNs         int64
	Closed         bool
	Discarded      bool
}

func (b *outputBatcher) snapshot() batcherSnapshot {
	b.mu.Lock()
	closed, discarded := b.closed, b.discarded
	b.mu.Unlock()
	return batcherSnapshot{
		RecvBytes:      b.stats.recvBytes.Load(),
		EmitBatches:    b.stats.emitBatches.Load(),
		EmitBytes:      b.stats.emitBytes.Load(),
		QueueLen:       b.stats.queueLen.Load(),
		QueuePeak:      b.stats.queuePeak.Load(),
		QueueWaitCount: b.stats.queueWaitCount.Load(),
		QueueWaitNs:    b.stats.queueWaitNs.Load(),
		SinkCalls:      b.stats.sinkCalls.Load(),
		SinkNs:         b.stats.sinkNs.Load(),
		Closed:         closed,
		Discarded:      discarded,
	}
}

func (b *outputBatcher) maybeLogMetrics(reason string) {
	if os.Getenv("NODESHELL_TERMINAL_METRICS") != "1" {
		return
	}
	s := b.snapshot()
	fmt.Fprintf(os.Stderr, "nodeshell: terminal-batcher %s recv_bytes=%d emit_batches=%d emit_bytes=%d queue_peak=%d queue_waits=%d wait_ns=%d sink_calls=%d sink_ns=%d closed=%t discarded=%t\n",
		reason, s.RecvBytes, s.EmitBatches, s.EmitBytes, s.QueuePeak, s.QueueWaitCount, s.QueueWaitNs, s.SinkCalls, s.SinkNs, s.Closed, s.Discarded)
}

// newOutputBatcher returns a batcher flushing at least every interval or when
// threshold bytes have accumulated. Adaptive coalescing is off: tests that
// pin exact flush sizes keep the historical fixed strategy.
func newOutputBatcher(interval time.Duration, threshold int, emit func([]byte)) *outputBatcher {
	return startBatcher(&outputBatcher{
		interval:   interval,
		threshold:  threshold,
		maxBatches: maxQueueBatches,
		emit:       emit,
		done:       make(chan struct{}),
	})
}

// newSessionBatcher is the production constructor: 12ms / 48KiB with adaptive
// coalescing under queue pressure, capped at 96KiB per batch.
func newSessionBatcher(emit func([]byte)) *outputBatcher {
	return startBatcher(&outputBatcher{
		interval:     flushInterval,
		threshold:    flushBytes,
		thresholdCap: maxFlushBytes,
		maxBatch:     maxFlushBytes,
		maxBatches:   maxQueueBatches,
		adaptive:     true,
		emit:         emit,
		done:         make(chan struct{}),
	})
}

func startBatcher(b *outputBatcher) *outputBatcher {
	b.cond = sync.NewCond(&b.mu)
	go b.run()
	return b
}

func (b *outputBatcher) run() {
	defer close(b.done)
	for {
		b.mu.Lock()
		for len(b.queue) == 0 && !b.closed && !b.discarded {
			b.cond.Wait()
		}
		if b.discarded {
			b.queue = nil
			b.stats.queueLen.Store(0)
			b.cond.Broadcast()
			b.mu.Unlock()
			return
		}
		if len(b.queue) == 0 && b.closed {
			b.cond.Broadcast()
			b.mu.Unlock()
			return
		}
		chunk := b.queue[0]
		b.queue = b.queue[1:]
		b.stats.queueLen.Store(int64(len(b.queue)))
		b.cond.Broadcast() // wake up any Add waiting on maxBatches
		b.mu.Unlock()

		// Deliver to sink outside mu without holding any lock. The sink must
		// not call back into this batcher (see type comment).
		start := time.Now()
		b.emit([]byte(toValidUTF8(string(chunk))))
		b.stats.sinkNs.Add(time.Since(start).Nanoseconds())
		b.stats.sinkCalls.Add(1)
		b.stats.emitBatches.Add(1)
		b.stats.emitBytes.Add(int64(len(chunk)))
	}
}

// toValidUTF8 replaces every maximal run of invalid UTF-8 bytes in s with a
// single U+FFFD, equivalent to unicode/utf8.ToValidString (absent from this
// toolchain).
func toValidUTF8(s string) string {
	if utf8.ValidString(s) {
		return s
	}
	var b strings.Builder
	b.Grow(len(s) + 8)
	invalid := false // previous byte was part of an invalid sequence
	for i := 0; i < len(s); {
		c := s[i]
		if c < utf8.RuneSelf {
			i++
			invalid = false
			b.WriteByte(c)
			continue
		}
		_, size := utf8.DecodeRuneInString(s[i:])
		if size == 1 {
			i++
			if !invalid {
				b.WriteString("\uFFFD")
				invalid = true
			}
			continue
		}
		b.WriteString(s[i : i+size])
		i += size
		invalid = false
	}
	return b.String()
}

// Add appends a raw read chunk to the pending buffer. If the batch queue is
// at capacity, Add blocks until the worker drains an item or teardown occurs.
func (b *outputBatcher) Add(p []byte) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed || b.discarded || len(p) == 0 {
		return
	}
	b.pending = append(b.pending, p...)
	b.stats.recvBytes.Add(int64(len(p)))
	b.flushReadyLocked()
	if len(b.pending) > 0 && !b.closed && !b.discarded {
		b.armTimerLocked()
	}
}

// flushReadyLocked moves complete pending bytes into cap-sized queue items
// while pending is at least the flush trigger. A single Add of hundreds of
// KiB therefore becomes several <=maxFlushBytes batches. The caller holds mu.
func (b *outputBatcher) flushReadyLocked() {
	for len(b.pending) >= b.flushThresholdLocked() {
		if !b.waitForQueueCapacityLocked(false) {
			return
		}
		out := b.takeLocked(b.batchCapLocked(), false)
		if len(out) == 0 {
			return
		}
		b.enqueueLocked(out)
	}
}

// flushThresholdLocked is the byte count that triggers an immediate enqueue.
// Under adaptive mode the threshold rises with queue depth so a slow sink
// receives fewer, larger batches. The interactive timer is unchanged.
func (b *outputBatcher) flushThresholdLocked() int {
	if !b.adaptive {
		return b.threshold
	}
	q := len(b.queue)
	if q < adaptiveQueueHigh {
		return b.threshold
	}
	span := b.maxBatches - adaptiveQueueHigh
	if span < 1 {
		span = 1
	}
	extra := b.threshold * (q - adaptiveQueueHigh + 1) / span
	t := b.threshold + extra
	capAt := b.thresholdCap
	if capAt <= 0 || capAt > maxFlushBytes {
		capAt = maxFlushBytes
	}
	if t > capAt {
		t = capAt
	}
	if t < b.threshold {
		return b.threshold
	}
	return t
}

// batchCapLocked is the hard per-batch byte limit. Adaptive coalescing may
// raise the *trigger* up to this value; takeLocked never returns more.
func (b *outputBatcher) batchCapLocked() int {
	capAt := b.maxBatch
	if capAt <= 0 || capAt > maxFlushBytes {
		return maxFlushBytes
	}
	return capAt
}

func (b *outputBatcher) enqueueLocked(out []byte) {
	b.queue = append(b.queue, out)
	n := int64(len(b.queue))
	b.stats.queueLen.Store(n)
	for {
		peak := b.stats.queuePeak.Load()
		if n <= peak || b.stats.queuePeak.CompareAndSwap(peak, n) {
			break
		}
	}
	b.cond.Signal()
}

// Close flushes any remaining bytes (replacing invalid/partial UTF-8) and
// stops the timer. Close blocks until all in-flight deliveries and the final
// batch have completed. All concurrent Close() callers wait until delivery is
// complete before returning. Idempotent.
func (b *outputBatcher) Close() {
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		<-b.done
		return
	}
	b.closed = true
	if b.timer != nil {
		b.timer.Stop()
		b.timerSet = false
	}
	for len(b.pending) > 0 && !b.discarded {
		if !b.waitForQueueCapacityLocked(true) {
			break
		}
		out := b.takeLocked(b.batchCapLocked(), true)
		if len(out) == 0 {
			out = append([]byte(nil), b.pending...)
			b.pending = nil
		}
		if len(out) > 0 {
			b.enqueueLocked(out)
		}
	}
	b.cond.Broadcast()
	b.mu.Unlock()

	<-b.done
	b.maybeLogMetrics("close")
}

// Discard is the no-emit close for quiet teardown (app shutdown): pending
// bytes are dropped and the timer stops. Discard blocks until any in-flight
// delivery finishes, guaranteeing zero emissions after Discard returns.
// All concurrent Discard() and Close() callers wait until teardown is
// complete before returning. Idempotent.
func (b *outputBatcher) Discard() {
	b.mu.Lock()
	b.discarded = true
	b.closed = true
	if b.timer != nil {
		b.timer.Stop()
		b.timerSet = false
	}
	// Clear these even when Close already won the state transition. Close may
	// be waiting for queue capacity with pending still referenced locally.
	// Discard is the terminal no-emit operation and must release both buffers.
	b.pending = nil
	b.queue = nil
	b.stats.queueLen.Store(0)
	b.cond.Broadcast()
	b.mu.Unlock()

	<-b.done
	b.maybeLogMetrics("discard")
}

// waitForQueueCapacityLocked waits until one queue slot is available. The
// caller must hold b.mu. Close may use allowClosed=true for its final batch;
// Discard always aborts the wait.
func (b *outputBatcher) waitForQueueCapacityLocked(allowClosed bool) bool {
	waited := false
	var start time.Time
	for len(b.queue) >= b.maxBatches && !b.discarded && (allowClosed || !b.closed) {
		if !waited {
			waited = true
			start = time.Now()
			b.stats.queueWaitCount.Add(1)
		}
		b.cond.Wait()
	}
	if waited {
		b.stats.queueWaitNs.Add(time.Since(start).Nanoseconds())
	}
	return !b.discarded && (allowClosed || !b.closed)
}

func (b *outputBatcher) armTimerLocked() {
	if b.timerSet || b.closed || b.discarded {
		return
	}
	b.timerSet = true
	b.timer = time.AfterFunc(b.interval, b.onTimer)
}

func (b *outputBatcher) onTimer() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.timerSet = false
	if b.closed || b.discarded {
		return
	}
	for {
		if !b.waitForQueueCapacityLocked(false) {
			return
		}
		out := b.takeLocked(b.batchCapLocked(), false)
		if len(out) > 0 {
			b.enqueueLocked(out)
		}
		if len(b.pending) == 0 || len(out) == 0 {
			break
		}
		// Leftover is either an incomplete rune or another cap-sized chunk.
		if len(b.pending) < b.batchCapLocked() && len(b.pending) < b.flushThresholdLocked() {
			break
		}
	}
	if len(b.pending) > 0 && !b.closed && !b.discarded {
		b.armTimerLocked()
	}
}

// takeLocked copies at most max bytes out of pending, never splitting a
// UTF-8 rune. Non-final takes also hold back a trailing incomplete sequence
// so a later Add can complete it. A final take (Close) emits the remainder,
// including an incomplete tail that emit will replace with U+FFFD.
// The caller must hold the mutex.
func (b *outputBatcher) takeLocked(max int, final bool) []byte {
	if len(b.pending) == 0 {
		return nil
	}
	if max <= 0 {
		max = maxFlushBytes
	}
	n := utf8PrefixLen(b.pending, max)
	if n == 0 {
		if !final {
			return nil
		}
		n = max
		if n > len(b.pending) {
			n = len(b.pending)
		}
	}
	if !final {
		n -= trailingPartialLen(b.pending[:n])
		if n == 0 {
			return nil
		}
	}
	out := append([]byte(nil), b.pending[:n]...)
	if n == len(b.pending) {
		b.pending = nil
	} else {
		b.pending = append([]byte(nil), b.pending[n:]...)
	}
	return out
}

// utf8PrefixLen is the largest n <= limit that does not split a UTF-8 rune.
// n == 0 means the first rune does not fit in limit (impossible at 96KiB).
func utf8PrefixLen(b []byte, limit int) int {
	if limit >= len(b) {
		return len(b)
	}
	if limit <= 0 {
		return 0
	}
	n := limit
	for n > 0 && !utf8.RuneStart(b[n]) {
		n--
	}
	return n
}

// trailingPartialLen reports how many trailing bytes of b form an incomplete
// UTF-8 sequence (to be held back), or 0 when the buffer ends on a clean
// boundary or with invalid-but-terminal bytes.
func trailingPartialLen(b []byte) int {
	n := len(b)
	if n == 0 {
		return 0
	}
	last := b[n-1]
	if last < utf8.RuneSelf {
		return 0 // ASCII can never be part of a multi-byte rune
	}
	// Walk back over continuation bytes to find the leading byte.
	i := n - 1
	for i > 0 && b[i] >= 0x80 && b[i] < 0xC0 {
		i--
	}
	lead := b[i]
	if lead >= 0xC0 && lead < 0xF8 {
		// Lead byte plus expected continuation bytes: RFC 3629 lengths.
		want := utf8Len(lead) - 1
		have := n - 1 - i
		if have < want {
			// Not enough continuation bytes yet — hold the whole tail back.
			return n - i
		}
	}
	return 0
}

// utf8Len returns the encoded length of a rune whose first byte is lead.
func utf8Len(lead byte) int {
	switch {
	case lead < 0xE0:
		return 2
	case lead < 0xF0:
		return 3
	default:
		return 4
	}
}

package sessions

import (
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

// Output batching constants mirror the Electron build's DATA_FLUSH_MS and
// DATA_FLUSH_BYTES.
const (
	flushInterval = 12 * time.Millisecond
	flushBytes    = 48 * 1024
)

// outputBatcher coalesces PTY output into bounded batches: it emits at most
// every flushInterval, or as soon as flushBytes have accumulated, and holds
// back a trailing partial UTF-8 sequence so a rune split across SSH reads is
// never emitted as U+FFFD. Every emitted chunk is valid UTF-8 — genuinely
// invalid bytes (and an incomplete rune at Close) are replaced with U+FFFD.
//
// The batcher is safe for concurrent Add from multiple pumps (stdout+stderr)
// and Close. Boundedness: pending bytes never exceed one read burst plus a
// held-back rune past the threshold, because every Add either emits or arms a
// short timer; no goroutine or timer outlives Close.
type outputBatcher struct {
	mu        sync.Mutex
	pending   []byte
	timer     *time.Timer
	timerSet  bool
	closed    bool
	interval  time.Duration
	threshold int
	emit      func(data []byte)
}

// newOutputBatcher returns a batcher flushing at least every interval or when
// threshold bytes have accumulated.
func newOutputBatcher(interval time.Duration, threshold int, emit func([]byte)) *outputBatcher {
	return &outputBatcher{interval: interval, threshold: threshold, emit: emit}
}

// send converts out to valid UTF-8 (invalid bytes replaced) and delivers it
// to the sink.
func (b *outputBatcher) send(out []byte) {
	b.emit([]byte(toValidUTF8(string(out))))
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

// Add appends a raw read chunk to the pending buffer.
func (b *outputBatcher) Add(p []byte) {
	b.mu.Lock()
	if b.closed || len(p) == 0 {
		b.mu.Unlock()
		return
	}
	b.pending = append(b.pending, p...)
	if len(b.pending) >= b.threshold {
		out := b.takeLocked()
		b.mu.Unlock()
		if len(out) > 0 {
			b.send(out)
		}
		return
	}
	b.armTimerLocked()
	b.mu.Unlock()
}

// Close flushes any remaining bytes (replacing invalid/partial UTF-8) and
// stops the timer. Idempotent.
func (b *outputBatcher) Close() {
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		return
	}
	b.closed = true
	if b.timer != nil {
		b.timer.Stop()
		b.timerSet = false
	}
	out := b.pending
	b.pending = nil
	b.mu.Unlock()
	if len(out) > 0 {
		b.send(out)
	}
}

// Discard is the no-emit close for quiet teardown (app shutdown): pending
// bytes are dropped and the timer stops. Idempotent; after Discard every Add
// is a no-op, so racing output pumps can never emit.
func (b *outputBatcher) Discard() {
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		return
	}
	b.closed = true
	if b.timer != nil {
		b.timer.Stop()
		b.timerSet = false
	}
	b.pending = nil
	b.mu.Unlock()
}

func (b *outputBatcher) armTimerLocked() {
	if b.timerSet || b.closed {
		return
	}
	b.timerSet = true
	b.timer = time.AfterFunc(b.interval, b.onTimer)
}

func (b *outputBatcher) onTimer() {
	b.mu.Lock()
	b.timerSet = false
	if b.closed {
		b.mu.Unlock()
		return
	}
	out := b.takeLocked()
	if len(b.pending) > 0 && !b.closed {
		b.armTimerLocked()
	}
	b.mu.Unlock()
	if len(out) > 0 {
		b.send(out)
	}
}

// takeLocked drains the pending buffer, holding back a trailing partial rune
// so it can complete in a later Add. The caller must hold the mutex.
func (b *outputBatcher) takeLocked() []byte {
	if len(b.pending) == 0 {
		return nil
	}
	n := trailingPartialLen(b.pending)
	if n == 0 {
		out := b.pending
		b.pending = nil
		return out
	}
	split := len(b.pending) - n
	out := b.pending[:split]
	b.pending = append([]byte(nil), b.pending[split:]...)
	return out
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

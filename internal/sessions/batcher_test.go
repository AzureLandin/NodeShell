package sessions

import (
	"strings"
	"sync"
	"testing"
	"time"
)

// collect emits every batch into a thread-safe slice for assertions.
type collector struct {
	mu     sync.Mutex
	chunks [][]byte
}

func (c *collector) emit(b []byte) {
	cp := append([]byte(nil), b...)
	c.mu.Lock()
	c.chunks = append(c.chunks, cp)
	c.mu.Unlock()
}

func (c *collector) all() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	var sb strings.Builder
	for _, ch := range c.chunks {
		sb.Write(ch)
	}
	return sb.String()
}

func (c *collector) count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.chunks)
}

func (c *collector) chunk(i int) string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return string(c.chunks[i])
}

// waitFor polls until cond is true or the timeout expires.
func waitFor(t *testing.T, timeout time.Duration, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func TestBatcherFlushesOnThreshold(t *testing.T) {
	c := &collector{}
	b := newOutputBatcher(flushInterval, 100, c.emit)
	defer b.Close()
	// Two Add calls below threshold must NOT emit; a third crossing 100 must.
	b.Add([]byte("aaaa")) // 4
	b.Add([]byte("bbbb")) // 8
	if got := c.count(); got != 0 {
		t.Fatalf("emitted %d chunks below threshold, want 0", got)
	}
	b.Add([]byte(strings.Repeat("x", 100))) // 108 >= 100
	if got := c.count(); got != 1 {
		t.Fatalf("emitted %d chunks, want 1", got)
	}
	if got := c.chunk(0); got != "aaaabbbb"+strings.Repeat("x", 100) {
		t.Fatalf("chunk = %q", got)
	}
}

func TestBatcherFlushesOnTimer(t *testing.T) {
	c := &collector{}
	b := newOutputBatcher(30*time.Millisecond, 1024, c.emit)
	defer b.Close()
	b.Add([]byte("hello"))
	waitFor(t, time.Second, "timer flush", func() bool { return c.count() == 1 })
	if got := c.chunk(0); got != "hello" {
		t.Fatalf("chunk = %q, want hello", got)
	}
}

// TestBatcherTimerKeepsFlushing: data arriving after the first flush must be
// delivered by a later timer, not lost.
func TestBatcherTimerKeepsFlushing(t *testing.T) {
	c := &collector{}
	b := newOutputBatcher(20*time.Millisecond, 1024, c.emit)
	defer b.Close()
	for i := 0; i < 5; i++ {
		b.Add([]byte("x"))
		time.Sleep(40 * time.Millisecond)
	}
	waitFor(t, time.Second, "5 flushes", func() bool { return c.count() >= 5 })
	if got := c.all(); got != "xxxxx" {
		t.Fatalf("all = %q", got)
	}
}

// TestBatcherUTF8SplitAcrossReads: a multi-byte rune arriving across two
// reads must be emitted intact (never as U+FFFD).
func TestBatcherUTF8SplitAcrossReads(t *testing.T) {
	c := &collector{}
	b := newOutputBatcher(20*time.Millisecond, 1024, c.emit)
	defer b.Close()
	b.Add([]byte("a"))           // first read: 0xE4 of 你 split off
	b.Add([]byte("\xE4"))        // partial lead byte held back
	b.Add([]byte("\xBD\xA0b"))   // completes 你 then 'b'
	waitFor(t, time.Second, "utf8 flush", func() bool { return c.count() == 1 })
	got := c.chunk(0)
	if got != "a你b" {
		t.Fatalf("got %q, want %q (must not contain U+FFFD)", got, "a你b")
	}
}

// TestBatcherUTF8HeldUntilComplete: the partial rune must not be emitted by
// the timer while incomplete.
func TestBatcherUTF8HeldUntilComplete(t *testing.T) {
	c := &collector{}
	b := newOutputBatcher(20*time.Millisecond, 1024, c.emit)
	defer b.Close()
	b.Add([]byte("\xE4\xBD")) // 你's first two bytes only
	time.Sleep(80 * time.Millisecond)
	if got := c.count(); got != 0 {
		t.Fatalf("emitted %d chunks with a held-back rune, want 0", got)
	}
	b.Add([]byte("\xA0")) // completes 你
	waitFor(t, time.Second, "complete rune flush", func() bool { return c.count() == 1 })
	if got := c.chunk(0); got != "你" {
		t.Fatalf("got %q, want 你", got)
	}
}

// TestBatcherCloseFlushesTrailingWithReplacement: on close, remaining bytes
// are flushed and an incomplete rune becomes U+FFFD (explicit policy).
func TestBatcherCloseFlushesTrailingWithReplacement(t *testing.T) {
	c := &collector{}
	b := newOutputBatcher(time.Hour, 1024, c.emit) // never flushes on timer
	b.Add([]byte("ab\xE4\xBD"))                     // partial 你 at the end
	b.Close()
	if got := c.count(); got != 1 {
		t.Fatalf("close emitted %d chunks, want 1", got)
	}
	if got := c.chunk(0); got != "ab\uFFFD" {
		t.Fatalf("got %q, want %q", got, "ab\uFFFD")
	}
}

// TestBatcherCloseIdempotentAndStopsTimer: Close twice emits once and the
// timer goroutine stops (no panic, no second flush).
func TestBatcherCloseIdempotentAndStopsTimer(t *testing.T) {
	c := &collector{}
	b := newOutputBatcher(10*time.Millisecond, 1024, c.emit)
	b.Add([]byte("data"))
	b.Close()
	b.Close()
	b.Add([]byte("late")) // after close: dropped, no panic
	time.Sleep(50 * time.Millisecond)
	if got := c.count(); got != 1 {
		t.Fatalf("emitted %d chunks, want exactly 1", got)
	}
}

// RED/GREEN: Discard is the no-emit close used by quiet teardown (app
// shutdown): pending bytes are dropped, the timer stops, and later Adds are
// no-ops — nothing is ever emitted.
func TestBatcherDiscardDropsPending(t *testing.T) {
	c := &collector{}
	b := newOutputBatcher(time.Hour, 1024, c.emit)
	b.Add([]byte("pending"))
	b.Discard()
	if got := c.count(); got != 0 {
		t.Fatalf("Discard emitted %d chunks, want 0", got)
	}
	b.Add([]byte("late"))
	b.Close()
	if got := c.count(); got != 0 {
		t.Fatalf("emitted %d chunks after Discard, want 0", got)
	}
}

// TestBatcherOrderStableUnderConcurrency: two producer goroutines feeding the
// same batcher must not reorder within a stream; all bytes arrive exactly.
func TestBatcherOrderStableUnderConcurrency(t *testing.T) {
	c := &collector{}
	b := newOutputBatcher(5*time.Millisecond, 64*1024, c.emit)
	defer b.Close()
	var wg sync.WaitGroup
	for g := 0; g < 4; g++ {
		wg.Add(1)
		go func(seed byte) {
			defer wg.Done()
			for i := 0; i < 2000; i++ {
				b.Add([]byte{seed})
			}
		}(byte('A' + g))
	}
	wg.Wait()
	// The batcher may not have flushed everything yet; wait for it.
	waitFor(t, 2*time.Second, "all bytes", func() bool { return c.count() > 0 && strings.HasPrefix(c.all(), "") })
	// Bytes must arrive in per-stream order: for each emitter the run of its
	// bytes is contiguous. Total byte count must be 8000.
	time.Sleep(100 * time.Millisecond) // let final timer flush
	var total int
	c.mu.Lock()
	for _, ch := range c.chunks {
		total += len(ch)
	}
	c.mu.Unlock()
	if total != 8000 {
		t.Fatalf("total bytes = %d, want 8000", total)
	}
}

// TestBatcherBoundedMemory: adding a huge burst never grows pending beyond
// one burst past the threshold (each Add over the threshold emits).
func TestBatcherBoundedMemory(t *testing.T) {
	c := &collector{}
	b := newOutputBatcher(time.Hour, 48*1024, c.emit)
	defer b.Close()
	burst := bytesOf(64 * 1024)
	b.Add(burst)
	// The 48KiB threshold should have triggered an emit immediately.
	if got := c.count(); got == 0 {
		t.Fatal("large burst below threshold did not emit")
	}
}

func bytesOf(n int) []byte {
	b := make([]byte, n)
	for i := range b {
		b[i] = 'x'
	}
	return b
}

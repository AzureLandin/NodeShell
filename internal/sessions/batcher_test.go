package sessions

import (
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf8"
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
	waitFor(t, time.Second, "threshold flush", func() bool { return c.count() == 1 })
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
	b.Add([]byte("a"))         // first read: 0xE4 of 你 split off
	b.Add([]byte("\xE4"))      // partial lead byte held back
	b.Add([]byte("\xBD\xA0b")) // completes 你 then 'b'
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
	b.Add([]byte("ab\xE4\xBD"))                    // partial 你 at the end
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

// TestBatcherDiscardDropsPending: Discard is the no-emit close used by quiet
// teardown: pending bytes are dropped, the timer stops, and later Adds are
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

// TestBatcherOrderStableUnderConcurrency: four producer goroutines feeding the
// same batcher must not lose or corrupt any bytes.
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
	// Wait for all 8000 bytes to be flushed and delivered.
	waitFor(t, 2*time.Second, "all bytes", func() bool { return len(c.all()) == 8000 })
	time.Sleep(50 * time.Millisecond)
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

// TestBatcherStrictMonotonicOrder: sequential tokens are delivered in exact order without reversal.
func TestBatcherStrictMonotonicOrder(t *testing.T) {
	c := &collector{}
	b := newOutputBatcher(5*time.Millisecond, 256, c.emit)
	defer b.Close()

	var expected strings.Builder
	for i := 1; i <= 500; i++ {
		token := fmt.Sprintf("%04d|", i)
		expected.WriteString(token)
		b.Add([]byte(token))
		if i%50 == 0 {
			time.Sleep(10 * time.Millisecond) // force timer flush
		}
	}
	waitFor(t, 2*time.Second, "sequential tokens", func() bool { return len(c.all()) == expected.Len() })
	if got := c.all(); got != expected.String() {
		t.Fatalf("output order corrupted: got len %d, want len %d", len(got), expected.Len())
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
	waitFor(t, time.Second, "large burst emit", func() bool { return c.count() >= 1 })
}

// TestBatcher4ByteUTF8MultiChunk: 4-byte UTF-8 rune fed 1 byte at a time across 4 Adds
// must be emitted cleanly without replacement characters.
func TestBatcher4ByteUTF8MultiChunk(t *testing.T) {
	c := &collector{}
	b := newOutputBatcher(20*time.Millisecond, 1024, c.emit)
	defer b.Close()
	// Rocket emoji 🚀 is \xF0\x9F\x9A\x80 (4 bytes)
	rocket := []byte("🚀")
	for _, byteVal := range rocket {
		b.Add([]byte{byteVal})
		time.Sleep(5 * time.Millisecond)
	}
	waitFor(t, time.Second, "4-byte utf8 flush", func() bool { return c.count() >= 1 })
	if got := c.all(); got != "🚀" {
		t.Fatalf("all = %q, want 🚀 (no U+FFFD)", got)
	}
}

// TestBatcherConcurrentAddAndClose: 8 goroutines calling Add while Close is called
// concurrently must not panic or cause data race, and NO emissions happen after Close returns.
func TestBatcherConcurrentAddAndClose(t *testing.T) {
	c := &collector{}
	b := newOutputBatcher(10*time.Millisecond, 256, c.emit)
	var wg sync.WaitGroup
	for g := 0; g < 8; g++ {
		wg.Add(1)
		go func(val byte) {
			defer wg.Done()
			for i := 0; i < 500; i++ {
				b.Add([]byte{val})
			}
		}(byte('a' + g))
	}
	time.Sleep(5 * time.Millisecond)
	b.Close()
	wg.Wait()

	countAtClose := c.count()
	time.Sleep(50 * time.Millisecond)
	if c.count() != countAtClose {
		t.Fatalf("emitted %d chunks after Close returned, want 0 late emits", c.count()-countAtClose)
	}

	// All emitted chunks must be valid UTF-8
	for i := 0; i < c.count(); i++ {
		ch := c.chunk(i)
		if !utf8.ValidString(ch) {
			t.Fatalf("chunk %d is not valid UTF-8: %q", i, ch)
		}
	}
}

// TestBatcherDiscardConcurrent: Discard racing with Adds guarantees 0 late emissions after Discard returns.
func TestBatcherDiscardConcurrent(t *testing.T) {
	c := &collector{}
	b := newOutputBatcher(5*time.Millisecond, 128, c.emit)
	var wg sync.WaitGroup
	for g := 0; g < 4; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 200; i++ {
				b.Add([]byte("sample"))
			}
		}()
	}
	b.Discard()
	wg.Wait()
	countAtDiscard := c.count()
	time.Sleep(50 * time.Millisecond)
	if c.count() != countAtDiscard {
		t.Fatalf("emitted %d chunks after Discard returned, want 0 late emits", c.count()-countAtDiscard)
	}
}

// TestBatcherSlowSinkCloseBlocksUntilDelivered: Close() must block until in-flight/pending data is emitted.
func TestBatcherSlowSinkCloseBlocksUntilDelivered(t *testing.T) {
	var emittedCount int64
	slowSink := func(data []byte) {
		time.Sleep(15 * time.Millisecond)
		atomic.AddInt64(&emittedCount, 1)
	}

	b := newOutputBatcher(time.Hour, 100, slowSink)
	b.Add([]byte(strings.Repeat("a", 150))) // triggers threshold emit
	b.Add([]byte("trailing"))

	// Close must block until trailing is emitted
	b.Close()
	if got := atomic.LoadInt64(&emittedCount); got != 2 {
		t.Fatalf("emittedCount = %d after Close(), want 2", got)
	}
}

// TestBatcherBlockingSinkConcurrentAddAndClose: multiple goroutines continuously Add
// while slow sink takes 5ms per chunk. When Close() returns, all emitted chunks are
// strictly in order, and zero chunks arrive after Close() returns.
func TestBatcherBlockingSinkConcurrentAddAndClose(t *testing.T) {
	c := &collector{}
	slowSink := func(data []byte) {
		time.Sleep(5 * time.Millisecond)
		c.emit(data)
	}

	b := newOutputBatcher(5*time.Millisecond, 64, slowSink)
	var wg sync.WaitGroup
	for g := 0; g < 4; g++ {
		wg.Add(1)
		go func(val byte) {
			defer wg.Done()
			for i := 0; i < 50; i++ {
				b.Add([]byte{val})
				time.Sleep(time.Millisecond)
			}
		}(byte('A' + g))
	}
	time.Sleep(10 * time.Millisecond)
	b.Close()
	wg.Wait()

	countAtClose := c.count()
	time.Sleep(50 * time.Millisecond)
	if c.count() != countAtClose {
		t.Fatalf("late emissions after Close: count=%d, countAtClose=%d", c.count(), countAtClose)
	}
}

// TestBatcherConcurrentCloseClose: multiple goroutines calling Close() concurrently
// all block until delivery completes before any Close() returns.
func TestBatcherConcurrentCloseClose(t *testing.T) {
	var inFlightCount int64
	var totalEmitted int64

	slowSink := func(data []byte) {
		atomic.AddInt64(&inFlightCount, 1)
		time.Sleep(10 * time.Millisecond)
		atomic.AddInt64(&inFlightCount, -1)
		atomic.AddInt64(&totalEmitted, 1)
	}

	b := newOutputBatcher(time.Hour, 50, slowSink)
	for i := 0; i < 5; i++ {
		b.Add([]byte(strings.Repeat("z", 60)))
	}

	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			b.Close()
			// When any Close() returns, inFlight must be 0
			if cur := atomic.LoadInt64(&inFlightCount); cur != 0 {
				t.Errorf("Close returned while emit was in-flight (inFlight=%d)", cur)
			}
		}()
	}
	wg.Wait()

	if got := atomic.LoadInt64(&totalEmitted); got != 5 {
		t.Fatalf("totalEmitted = %d, want 5", got)
	}
}

// TestBatcherConcurrentCloseDiscard: Close racing with Discard guarantees
// all callers wait until worker has completely terminated, with 0 late emits.
func TestBatcherConcurrentCloseDiscard(t *testing.T) {
	c := &collector{}
	slowSink := func(data []byte) {
		time.Sleep(5 * time.Millisecond)
		c.emit(data)
	}

	b := newOutputBatcher(5*time.Millisecond, 32, slowSink)
	for i := 0; i < 10; i++ {
		b.Add([]byte("sample-data-chunk"))
	}

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		b.Close()
	}()
	go func() {
		defer wg.Done()
		b.Discard()
	}()
	wg.Wait()

	countAtTeardown := c.count()
	time.Sleep(30 * time.Millisecond)
	if c.count() != countAtTeardown {
		t.Fatalf("emissions occurred after Close/Discard returned: %d != %d", c.count(), countAtTeardown)
	}
}

// TestBatcherSinkRunsOutsideMutex verifies the worker never holds the batcher
// mutex while invoking the sink. Synchronous re-entry into this batcher remains
// explicitly unsupported because a re-entrant enqueue can self-deadlock when
// the bounded queue is full.
func TestBatcherSinkRunsOutsideMutex(t *testing.T) {
	var b *outputBatcher
	unlocked := make(chan bool, 1)
	sink := func(data []byte) {
		if b.mu.TryLock() {
			b.mu.Unlock()
			unlocked <- true
			return
		}
		unlocked <- false
	}

	b = newOutputBatcher(time.Hour, 10, sink)
	b.Add([]byte("threshold-12345"))
	select {
	case got := <-unlocked:
		if !got {
			t.Fatal("sink was invoked while batcher mutex was held")
		}
	case <-time.After(time.Second):
		t.Fatal("sink was not invoked")
	}
	b.Close()
}

// TestBatcherBoundedQueueBackpressure: Add blocks when queue reaches max capacity,
// exerting backpressure without losing data.
func TestBatcherBoundedQueueBackpressure(t *testing.T) {
	c := &collector{}
	slowSink := func(data []byte) {
		time.Sleep(10 * time.Millisecond)
		c.emit(data)
	}

	b := newOutputBatcher(time.Hour, 10, slowSink)
	b.maxBatches = 2 // small max batches for test

	var wg sync.WaitGroup
	for i := 0; i < 6; i++ {
		wg.Add(1)
		go func(val int) {
			defer wg.Done()
			b.Add([]byte(fmt.Sprintf("item-%d-123456", val)))
		}(i)
	}
	wg.Wait()
	b.Close()

	got := c.all()
	var want strings.Builder
	for i := 0; i < 6; i++ {
		want.WriteString(fmt.Sprintf("item-%d-123456", i))
	}
	if len(got) != want.Len() {
		t.Fatalf("total bytes = %d, want %d; output=%q", len(got), want.Len(), got)
	}
	for i := 0; i < 6; i++ {
		token := fmt.Sprintf("item-%d-123456", i)
		if !strings.Contains(got, token) {
			t.Fatalf("output lost token %q: %q", token, got)
		}
	}
}

func bytesOf(n int) []byte {
	b := make([]byte, n)
	for i := range b {
		b[i] = 'x'
	}
	return b
}

func TestBatcherMixedCorpusPreservesBytes(t *testing.T) {
	chunks := mixedOutputChunks()
	want := string(joinChunks(chunks))
	c := &collector{}
	b := newOutputBatcher(5*time.Millisecond, 64, c.emit)
	for _, ch := range chunks {
		b.Add(ch)
	}
	b.Close()
	if got := c.all(); got != want {
		t.Fatalf("corpus mismatch: got %q want %q", got, want)
	}
}

func TestBatcherMetricsCountRecvAndEmit(t *testing.T) {
	c := &collector{}
	b := newOutputBatcher(time.Hour, 8, c.emit)
	b.Add([]byte("12345678"))
	b.Add([]byte("abcd"))
	b.Close()
	s := b.snapshot()
	if s.RecvBytes != 12 {
		t.Fatalf("recvBytes=%d want 12", s.RecvBytes)
	}
	if s.EmitBytes != 12 {
		t.Fatalf("emitBytes=%d want 12", s.EmitBytes)
	}
	if s.EmitBatches != 2 {
		t.Fatalf("emitBatches=%d want 2", s.EmitBatches)
	}
	if s.SinkCalls != 2 {
		t.Fatalf("sinkCalls=%d want 2", s.SinkCalls)
	}
	if !s.Closed || s.Discarded {
		t.Fatalf("snapshot closed=%t discarded=%t", s.Closed, s.Discarded)
	}
	if s.QueuePeak < 1 {
		t.Fatalf("queuePeak=%d want >=1", s.QueuePeak)
	}
}

func TestBatcherDiscardSnapshotFlags(t *testing.T) {
	b := newOutputBatcher(time.Hour, 8, func([]byte) {})
	b.Add([]byte("12345678"))
	b.Discard()
	s := b.snapshot()
	if !s.Discarded || !s.Closed {
		t.Fatalf("snapshot closed=%t discarded=%t", s.Closed, s.Discarded)
	}
	if s.EmitBatches != 0 {
		t.Fatalf("discard must not emit, got %d batches", s.EmitBatches)
	}
}

func TestBatcherSlowSinkAddBackpressure(t *testing.T) {
	entered := make(chan struct{}, 8)
	release := make(chan struct{})
	b := newOutputBatcher(time.Hour, 8, func([]byte) {
		entered <- struct{}{}
		<-release
	})
	b.maxBatches = 1
	b.Add([]byte("12345678"))
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("sink was not entered")
	}
	b.Add([]byte("abcdefgh")) // queue full while worker is in sink

	finished := make(chan struct{})
	go func() {
		b.Add([]byte("ijklmnop"))
		close(finished)
	}()
	waitFor(t, time.Second, "queue waiter", func() bool {
		return b.stats.queueWaitCount.Load() > 0
	})
	select {
	case <-finished:
		t.Fatal("Add returned before a queue slot was free")
	default:
	}
	close(release)
	select {
	case <-finished:
	case <-time.After(time.Second):
		t.Fatal("Add did not unblock after sink released")
	}
	b.Close()
}

func TestBatcherDiscardWakesQueueWaiters(t *testing.T) {
	entered := make(chan struct{}, 8)
	release := make(chan struct{})
	b := newOutputBatcher(time.Hour, 8, func([]byte) {
		entered <- struct{}{}
		<-release
	})
	b.maxBatches = 1
	b.Add([]byte("12345678"))
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("sink was not entered")
	}
	b.Add([]byte("abcdefgh"))

	finished := make(chan struct{})
	go func() {
		b.Add([]byte("ijklmnop"))
		close(finished)
	}()
	waitFor(t, time.Second, "queue waiter", func() bool {
		return b.stats.queueWaitCount.Load() > 0
	})
	go func() {
		// Unblock the in-flight sink so Discard can join the worker.
		close(release)
	}()
	b.Discard()
	select {
	case <-finished:
	case <-time.After(time.Second):
		t.Fatal("Discard did not wake the Add waiter")
	}
}

func TestBatcherCloseWaitsInFlightSink(t *testing.T) {
	entered := make(chan struct{})
	release := make(chan struct{})
	var finished atomic.Bool
	b := newOutputBatcher(time.Hour, 8, func([]byte) {
		close(entered)
		<-release
		finished.Store(true)
	})
	b.Add([]byte("12345678"))
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("sink was not entered")
	}
	closed := make(chan struct{})
	go func() {
		b.Close()
		close(closed)
	}()
	select {
	case <-closed:
		t.Fatal("Close returned while sink was still running")
	case <-time.After(20 * time.Millisecond):
	}
	close(release)
	select {
	case <-closed:
	case <-time.After(time.Second):
		t.Fatal("Close did not return after sink finished")
	}
	if !finished.Load() {
		t.Fatal("sink did not finish before Close returned")
	}
}

func TestBatcherAcceptedBytesSurviveClose(t *testing.T) {
	c := &collector{}
	b := newOutputBatcher(5*time.Millisecond, 32, c.emit)
	var wg sync.WaitGroup
	const n = 4
	const per = 200
	for g := 0; g < n; g++ {
		wg.Add(1)
		go func(seed byte) {
			defer wg.Done()
			buf := []byte{seed}
			for i := 0; i < per; i++ {
				b.Add(buf)
			}
		}(byte('A' + g))
	}
	wg.Wait()
	b.Close()
	if got := len(c.all()); got != n*per {
		t.Fatalf("accepted bytes lost: got %d want %d", got, n*per)
	}
}

func TestBatcherAdaptiveMatchesFixedBytes(t *testing.T) {
	chunks := repeatChunks(mixedOutputChunks(), 8)
	want := string(joinChunks(chunks))

	run := func(adaptive bool) string {
		c := &collector{}
		var b *outputBatcher
		if adaptive {
			b = newSessionBatcher(c.emit)
		} else {
			b = newOutputBatcher(flushInterval, flushBytes, c.emit)
		}
		for _, ch := range chunks {
			b.Add(ch)
		}
		b.Close()
		return c.all()
	}

	fixed := run(false)
	adaptive := run(true)
	if fixed != want {
		t.Fatalf("fixed strategy corrupted corpus")
	}
	if adaptive != want {
		t.Fatalf("adaptive strategy corrupted corpus")
	}
	if adaptive != fixed {
		t.Fatal("adaptive output bytes differ from fixed strategy")
	}
}

func TestBatcherAdaptiveFewerBatchesUnderBackpressure(t *testing.T) {
	chunks := make([][]byte, 24)
	for i := range chunks {
		chunks[i] = bytesOf(48 * 1024)
	}
	run := func(adaptive bool) batcherSnapshot {
		var b *outputBatcher
		if adaptive {
			b = newSessionBatcher(func([]byte) { time.Sleep(2 * time.Millisecond) })
		} else {
			b = newOutputBatcher(flushInterval, flushBytes, func([]byte) { time.Sleep(2 * time.Millisecond) })
		}
		for _, ch := range chunks {
			b.Add(ch)
		}
		b.Close()
		return b.snapshot()
	}
	fixed := run(false)
	adaptive := run(true)
	if adaptive.RecvBytes != fixed.RecvBytes {
		t.Fatalf("recvBytes adaptive=%d fixed=%d", adaptive.RecvBytes, fixed.RecvBytes)
	}
	if adaptive.EmitBytes != fixed.EmitBytes {
		t.Fatalf("emitBytes adaptive=%d fixed=%d", adaptive.EmitBytes, fixed.EmitBytes)
	}
	if adaptive.EmitBatches > fixed.EmitBatches {
		t.Fatalf("adaptive emitted more batches (%d) than fixed (%d)", adaptive.EmitBatches, fixed.EmitBatches)
	}
}

func assertBatchCap(t *testing.T, c *collector, capBytes int) {
	t.Helper()
	for i := 0; i < c.count(); i++ {
		if n := len(c.chunk(i)); n > capBytes {
			t.Fatalf("batch %d len %d exceeds cap %d", i, n, capBytes)
		}
	}
}

func TestBatcherHardCapSingleAddOverMax(t *testing.T) {
	c := &collector{}
	b := newOutputBatcher(time.Hour, 48*1024, c.emit)
	payload := bytesOf(200 * 1024)
	b.Add(payload)
	b.Close()
	if got := c.all(); got != string(payload) {
		t.Fatalf("lost or duplicated bytes: got %d want %d", len(got), len(payload))
	}
	assertBatchCap(t, c, maxFlushBytes)
	if c.count() < 3 {
		t.Fatalf("expected at least 3 batches for 200KiB, got %d", c.count())
	}
}

func TestBatcherHardCapAccumulatedAdds(t *testing.T) {
	c := &collector{}
	// Threshold above one chunk so the first Add stays pending; the second
	// crosses the trigger with 140KiB already buffered.
	b := newOutputBatcher(time.Hour, 100*1024, c.emit)
	first := bytesOf(70 * 1024)
	second := bytesOf(70 * 1024)
	b.Add(first)
	if c.count() != 0 {
		t.Fatalf("first Add flushed early: %d batches", c.count())
	}
	b.Add(second)
	b.Close()
	want := string(first) + string(second)
	if got := c.all(); got != want {
		t.Fatalf("lost or duplicated bytes: got %d want %d", len(got), len(want))
	}
	assertBatchCap(t, c, maxFlushBytes)
}

func TestBatcherHardCapUTF8Boundary(t *testing.T) {
	c := &collector{}
	b := newOutputBatcher(time.Hour, 1, c.emit)
	// 96KiB-1 ASCII plus a 3-byte rune that would straddle the cap.
	payload := append(bytesOf(maxFlushBytes-1), []byte("你")...)
	b.Add(payload)
	b.Close()
	if got := c.all(); got != string(payload) {
		t.Fatalf("utf8 split corrupted output: got %q", got)
	}
	assertBatchCap(t, c, maxFlushBytes)
	if c.count() < 2 {
		t.Fatalf("expected a split at the rune, got %d batches", c.count())
	}
	if !utf8.ValidString(c.chunk(0)) {
		t.Fatalf("first batch is not valid UTF-8: %q", c.chunk(0))
	}
	if c.chunk(0)[len(c.chunk(0))-1] == 0xE4 {
		t.Fatal("first batch ended on 你's lead byte")
	}
	if !strings.Contains(c.all(), "你") {
		t.Fatal("missing 你 after split")
	}
}

func TestBatcherHardCapUTF8WithSmallMaxBatch(t *testing.T) {
	c := &collector{}
	b := newOutputBatcher(time.Hour, 1, c.emit)
	b.maxBatch = 8
	// 6 ASCII + 你 (3 bytes) = 9, so the rune sits across the 8-byte cap.
	payload := append([]byte("aaaaaa"), []byte("你")...)
	b.Add(payload)
	b.Close()
	if got := c.all(); got != string(payload) {
		t.Fatalf("got %q want %q", got, payload)
	}
	assertBatchCap(t, c, 8)
	if c.chunk(0) != "aaaaaa" {
		t.Fatalf("first batch = %q, want aaaaaa (rune must not be split)", c.chunk(0))
	}
	if c.chunk(1) != "你" {
		t.Fatalf("second batch = %q, want 你", c.chunk(1))
	}
}

func TestBatcherHardCapCloseHugePending(t *testing.T) {
	c := &collector{}
	// Threshold far above the payload so Add never flushes; Close must split.
	b := newOutputBatcher(time.Hour, 1024*1024, c.emit)
	payload := bytesOf(200 * 1024)
	b.Add(payload)
	if c.count() != 0 {
		t.Fatalf("Add flushed before Close: %d", c.count())
	}
	b.Close()
	if got := c.all(); got != string(payload) {
		t.Fatalf("close lost bytes: got %d want %d", len(got), len(payload))
	}
	assertBatchCap(t, c, maxFlushBytes)
	if c.count() < 3 {
		t.Fatalf("close should split 200KiB, got %d batches", c.count())
	}
}

func TestBatcherHardCapFixedAndAdaptiveNoLoss(t *testing.T) {
	chunks := [][]byte{
		bytesOf(80 * 1024),
		bytesOf(80 * 1024),
		append(bytesOf(maxFlushBytes-2), []byte("🚀")...),
		[]byte("tail"),
	}
	want := string(joinChunks(chunks))
	run := func(adaptive bool) (string, []int) {
		c := &collector{}
		var b *outputBatcher
		if adaptive {
			b = newSessionBatcher(c.emit)
		} else {
			b = newOutputBatcher(flushInterval, flushBytes, c.emit)
		}
		for _, ch := range chunks {
			b.Add(ch)
		}
		b.Close()
		sizes := make([]int, c.count())
		for i := range sizes {
			sizes[i] = len(c.chunk(i))
		}
		return c.all(), sizes
	}
	fixed, fixedSizes := run(false)
	adaptive, adaptiveSizes := run(true)
	if fixed != want {
		t.Fatalf("fixed lost/duplicated data: got %d want %d", len(fixed), len(want))
	}
	if adaptive != want {
		t.Fatalf("adaptive lost/duplicated data: got %d want %d", len(adaptive), len(want))
	}
	if fixed != adaptive {
		t.Fatal("adaptive bytes differ from fixed")
	}
	for i, n := range fixedSizes {
		if n > maxFlushBytes {
			t.Fatalf("fixed batch %d len %d > %d", i, n, maxFlushBytes)
		}
	}
	for i, n := range adaptiveSizes {
		if n > maxFlushBytes {
			t.Fatalf("adaptive batch %d len %d > %d", i, n, maxFlushBytes)
		}
	}
}

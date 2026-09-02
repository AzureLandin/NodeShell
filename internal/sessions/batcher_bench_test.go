package sessions

import (
	"testing"
	"time"
)

func benchAddAll(b *testing.B, interval time.Duration, threshold int, chunks [][]byte, sinkDelay time.Duration, adaptive bool) {
	b.Helper()
	payload := joinChunks(chunks)
	b.SetBytes(int64(len(payload)))
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		var bat *outputBatcher
		if adaptive {
			bat = newSessionBatcher(func([]byte) {
				if sinkDelay > 0 {
					time.Sleep(sinkDelay)
				}
			})
		} else {
			bat = newOutputBatcher(interval, threshold, func([]byte) {
				if sinkDelay > 0 {
					time.Sleep(sinkDelay)
				}
			})
		}
		for _, ch := range chunks {
			bat.Add(ch)
		}
		bat.Close()
	}
}

func BenchmarkBatcherSmallSlow(b *testing.B) {
	chunks := make([][]byte, 64)
	for i := range chunks {
		chunks[i] = []byte("x")
	}
	benchAddAll(b, flushInterval, flushBytes, chunks, 0, false)
}

func BenchmarkBatcherThresholdBurst(b *testing.B) {
	benchAddAll(b, flushInterval, flushBytes, [][]byte{bytesOf(48 * 1024)}, 0, false)
}

func BenchmarkBatcherSustainedHighRate(b *testing.B) {
	chunks := make([][]byte, 64)
	for i := range chunks {
		chunks[i] = bytesOf(32 * 1024)
	}
	benchAddAll(b, flushInterval, flushBytes, chunks, 0, false)
}

func BenchmarkBatcherSlowSinkBackpressure(b *testing.B) {
	chunks := make([][]byte, 16)
	for i := range chunks {
		chunks[i] = bytesOf(48 * 1024)
	}
	benchAddAll(b, flushInterval, flushBytes, chunks, time.Millisecond, false)
}

func BenchmarkBatcherMixedCorpus(b *testing.B) {
	benchAddAll(b, flushInterval, flushBytes, repeatChunks(mixedOutputChunks(), 32), 0, false)
}

func BenchmarkBatcherAdaptiveSustainedHighRate(b *testing.B) {
	chunks := make([][]byte, 64)
	for i := range chunks {
		chunks[i] = bytesOf(32 * 1024)
	}
	benchAddAll(b, flushInterval, flushBytes, chunks, 0, true)
}

func BenchmarkBatcherAdaptiveSlowSinkBackpressure(b *testing.B) {
	chunks := make([][]byte, 16)
	for i := range chunks {
		chunks[i] = bytesOf(48 * 1024)
	}
	benchAddAll(b, flushInterval, flushBytes, chunks, time.Millisecond, true)
}

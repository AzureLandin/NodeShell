package sessions

import "bytes"

// mixedOutputChunks is a repeatable PTY-like corpus: ASCII, CJK, emoji,
// ANSI CSI sequences, and a UTF-8 rune split across two Adds. Callers that
// need a single contiguous stream must concatenate in this order.
func mixedOutputChunks() [][]byte {
	return [][]byte{
		[]byte("hello world\n"),
		[]byte("中文输出测试ABCDEF"),
		[]byte("🚀✨中\n"),
		[]byte("\x1b[1;32mOK\x1b[0m \x1b[2J\x1b[H\x1b[31mred\x1b[0m\n"),
		[]byte("pre\xE4"), // first byte of 你
		[]byte("\xBD\xA0post\n"),
		bytesOf(1024),
		[]byte("\xF0\x9F"), // first two bytes of 🚀
		[]byte("\x9A\x80tail\n"),
	}
}

func joinChunks(chunks [][]byte) []byte {
	return bytes.Join(chunks, nil)
}

func repeatChunks(chunks [][]byte, n int) [][]byte {
	out := make([][]byte, 0, len(chunks)*n)
	for i := 0; i < n; i++ {
		out = append(out, chunks...)
	}
	return out
}

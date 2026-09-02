import { describe, expect, it } from 'vitest'
import { MAX_WRITE_CHARS_PER_FRAME, takeWriteChunk } from '../src/renderer/src/terminal-output'

describe('takeWriteChunk', () => {
  it('returns the whole string when it fits in one frame', () => {
    expect(takeWriteChunk('abc', 10)).toEqual({ chunk: 'abc', rest: '' })
    expect(takeWriteChunk('', 10)).toEqual({ chunk: '', rest: '' })
  })

  it('splits on the cap and keeps the remainder queued', () => {
    expect(takeWriteChunk('abcdefghij', 4)).toEqual({ chunk: 'abcd', rest: 'efghij' })
  })

  it('does not split a UTF-16 surrogate pair at the cap', () => {
    const rocket = '🚀'
    const pending = 'ab' + rocket + 'cd'
    // 'ab' + high surrogate would be 3 code units; rocket is 2 units.
    const { chunk, rest } = takeWriteChunk(pending, 3)
    expect(chunk + rest).toBe(pending)
    expect(chunk.includes('\ud83d') && !chunk.includes('\ude80')).toBe(false)
    expect([...chunk].concat([...rest]).join('')).toBe(pending)
  })

  it('uses the exported per-frame cap by default', () => {
    const pending = 'x'.repeat(MAX_WRITE_CHARS_PER_FRAME + 12)
    const { chunk, rest } = takeWriteChunk(pending)
    expect(chunk.length).toBe(MAX_WRITE_CHARS_PER_FRAME)
    expect(rest.length).toBe(12)
    expect(chunk + rest).toBe(pending)
  })
})

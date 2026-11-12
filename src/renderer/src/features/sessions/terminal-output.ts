/**
 * Terminal output write helpers. Kept out of TerminalView so UTF-16 chunking
 * and per-frame write caps can be unit-tested without mounting xterm.
 *
 * The cap is in UTF-16 code units (JS string length), matching the existing
 * frontend metrics which count `chunk.length` rather than UTF-8 bytes.
 */

/** Max characters written to xterm in one animation frame. Remainder is queued. */
export const MAX_WRITE_CHARS_PER_FRAME = 256 * 1024

/**
 * Take a write-sized prefix of pending output without splitting a UTF-16
 * surrogate pair. Empty pending or a non-positive cap returns the whole
 * string as the chunk.
 */
export function takeWriteChunk(
  pending: string,
  maxChars: number = MAX_WRITE_CHARS_PER_FRAME
): { chunk: string; rest: string } {
  if (pending.length === 0) {
    return { chunk: '', rest: '' }
  }
  if (maxChars <= 0 || pending.length <= maxChars) {
    return { chunk: pending, rest: '' }
  }
  let end = maxChars
  const last = pending.charCodeAt(end - 1)
  if (last >= 0xd800 && last <= 0xdbff) {
    end -= 1
  }
  if (end <= 0) {
    const first = pending.charCodeAt(0)
    end = first >= 0xd800 && first <= 0xdbff && pending.length >= 2 ? 2 : 1
  }
  return { chunk: pending.slice(0, end), rest: pending.slice(end) }
}

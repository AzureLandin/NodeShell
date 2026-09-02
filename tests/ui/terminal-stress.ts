/** Repeatable terminal payloads for P2 stress tests. No secrets, no paths. */

export function asciiBlock(n: number): string {
  return 'abcdefghijklmnopqrstuvwxyz\n'.repeat(Math.max(1, Math.ceil(n / 27))).slice(0, n)
}

export function chineseBlock(): string {
  return '中文输出测试段落，用于验证终端在多字节字符下的顺序与完整性。\n'
}

export function emojiBlock(): string {
  return '🚀✨中😀👨\u{1F468}\u{200D}\u{1F469}\u{200D}\u{1F467}\n'
}

export function ansiBlock(): string {
  return '\x1b[1;32mOK\x1b[0m \x1b[2J\x1b[H\x1b[31mred\x1b[0m\n'
}

export function mixedTerminalPayloads(): string[] {
  return [asciiBlock(64), chineseBlock(), emojiBlock(), ansiBlock(), 'pre你post\n']
}

export function burstPayloads(prefix: string, count: number): string[] {
  const mixed = mixedTerminalPayloads()
  const out: string[] = []
  for (let i = 0; i < count; i++) {
    out.push(`${prefix}-${i}:${mixed[i % mixed.length]}`)
  }
  return out
}

import type { ITheme } from '@xterm/xterm'
import type { ResolvedTheme } from '../../../../shared/types'

/** Atom One Dark–inspired xterm palette. */
export const ONE_DARK_THEME: ITheme = {
  background: '#1e1e1e',
  foreground: '#abb2bf',
  cursor: '#528bff',
  cursorAccent: '#1e1e1e',
  selectionBackground: '#3e4451',
  black: '#282c34',
  red: '#e06c75',
  green: '#98c379',
  yellow: '#e5c07b',
  blue: '#61afef',
  magenta: '#c678dd',
  cyan: '#56b6c2',
  white: '#abb2bf',
  brightBlack: '#5c6370',
  brightRed: '#e06c75',
  brightGreen: '#98c379',
  brightYellow: '#e5c07b',
  brightBlue: '#61afef',
  brightMagenta: '#c678dd',
  brightCyan: '#56b6c2',
  brightWhite: '#ffffff'
}

/** Atom One Light–inspired xterm palette. */
export const ONE_LIGHT_THEME: ITheme = {
  background: '#fafafa',
  foreground: '#383a42',
  cursor: '#526fff',
  cursorAccent: '#fafafa',
  selectionBackground: '#e5e5e6',
  black: '#383a42',
  red: '#e45649',
  green: '#50a14f',
  yellow: '#c18401',
  blue: '#4078f2',
  magenta: '#a626a4',
  cyan: '#0184bc',
  white: '#fafafa',
  brightBlack: '#9d9d9f',
  brightRed: '#e45649',
  brightGreen: '#50a14f',
  brightYellow: '#c18401',
  brightBlue: '#4078f2',
  brightMagenta: '#a626a4',
  brightCyan: '#0184bc',
  brightWhite: '#ffffff'
}

export function getTerminalTheme(resolved: ResolvedTheme): ITheme {
  return resolved === 'light' ? ONE_LIGHT_THEME : ONE_DARK_THEME
}

export function buildTerminalFontStack(family: string): string {
  const primary = family.trim() || 'Hack'
  if (primary === 'monospace') return 'monospace'
  const safe = primary.replace(/"/g, '')
  return `"${safe}", Consolas, "Courier New", monospace`
}

export const TERMINAL_FONT_SIZE_MIN = 10
export const TERMINAL_FONT_SIZE_MAX = 24

export function clampTerminalFontSize(size: number): number {
  if (!Number.isFinite(size)) return 14
  return Math.min(TERMINAL_FONT_SIZE_MAX, Math.max(TERMINAL_FONT_SIZE_MIN, Math.round(size)))
}

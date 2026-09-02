import { useEffect, useRef, useState } from 'react'
import { FitAddon } from '@xterm/addon-fit'
import { UnicodeGraphemesAddon } from '@xterm/addon-unicode-graphemes'
import { WebglAddon } from '@xterm/addon-webgl'
import { Terminal } from '@xterm/xterm'
import '@xterm/xterm/css/xterm.css'
import type { ResolvedTheme } from '../../../shared/types'
import { TerminalContextMenu } from './TerminalContextMenu'
import { buildTerminalFontStack, clampTerminalFontSize, getTerminalTheme } from '../terminal-theme'
import { MAX_WRITE_CHARS_PER_FRAME, takeWriteChunk } from '../terminal-output'

/** Cap scrollback so the active terminal stays bounded in memory. */
const TERMINAL_SCROLLBACK = 1000

/** Debounce window for layout changes (window resize, sidebar / SFTP animations). */
const RESIZE_DEBOUNCE_MS = 120

/** Automatic retry delay after an IPC resize failure. */
const RESIZE_RETRY_DELAY_MS = 500

interface TerminalSize {
  cols: number
  rows: number
}

export interface TerminalMetrics {
  flushCount: number
  totalBytesWritten: number
  peakPendingBytes: number
  lastFlushBytes: number
  dataReceiveCount: number
  writeCount: number
  resizeObserverCount: number
  resizeIpcCount: number
  webglFallbackCount: number
}

declare global {
  interface Window {
    __TERMINAL_METRICS__?: Record<string, TerminalMetrics>
  }
}

function emptyMetrics(): TerminalMetrics {
  return {
    flushCount: 0,
    totalBytesWritten: 0,
    peakPendingBytes: 0,
    lastFlushBytes: 0,
    dataReceiveCount: 0,
    writeCount: 0,
    resizeObserverCount: 0,
    resizeIpcCount: 0,
    webglFallbackCount: 0
  }
}

function publishMetrics(sessionId: string, metrics: TerminalMetrics): void {
  if (!window.__TERMINAL_METRICS__) return
  window.__TERMINAL_METRICS__[sessionId] = { ...metrics }
}

/** GPU renderer when WebGL2 is available; dispose on context loss to fall back to DOM once. */
function tryEnableWebglRenderer(
  term: Terminal,
  degradedRef: { current: boolean },
  onFallback: () => void
): WebglAddon | null {
  if (degradedRef.current) return null
  try {
    const webgl = new WebglAddon()
    webgl.onContextLoss(() => {
      if (degradedRef.current) return
      degradedRef.current = true
      onFallback()
      try {
        webgl.dispose()
      } catch {
        /* ignore */
      }
    })
    term.loadAddon(webgl)
    return webgl
  } catch {
    /* jsdom, old GPU, or missing WebGL2 — keep the DOM renderer */
    degradedRef.current = true
    onFallback()
    return null
  }
}

interface TerminalViewProps {
  sessionId: string
  registerDataListener: (sessionId: string, cb: (data: string) => void) => () => void
  visible: boolean
  fontFamily: string
  fontSize: number
  resolvedTheme: ResolvedTheme
  onFontSizeChange?: (size: number) => void
}

export function TerminalView({
  sessionId,
  registerDataListener,
  visible,
  fontFamily,
  fontSize,
  resolvedTheme,
  onFontSizeChange
}: TerminalViewProps): React.JSX.Element {
  const containerRef = useRef<HTMLDivElement>(null)
  const termRef = useRef<Terminal | null>(null)
  const fitRef = useRef<FitAddon | null>(null)
  const webglRef = useRef<WebglAddon | null>(null)
  const webglDegradedRef = useRef(false)
  const fontSizeRef = useRef(fontSize)
  const onFontSizeChangeRef = useRef(onFontSizeChange)
  const resolvedThemeRef = useRef(resolvedTheme)
  const visibleRef = useRef(visible)
  const disposedRef = useRef(false)

  // Remote resize state machine: deduplication, queueing, and failure retry
  const desiredSizeRef = useRef<TerminalSize | null>(null)
  const acknowledgedSizeRef = useRef<TerminalSize | null>(null)
  const inFlightSizeRef = useRef<TerminalSize | null>(null)
  const fitTimerRef = useRef<number | null>(null)
  const retryTimerRef = useRef<number | null>(null)

  // Lightweight performance & backlog telemetry. Always counted; published
  // only when tests/diagnostics set window.__TERMINAL_METRICS__.
  const metricsRef = useRef<TerminalMetrics>(emptyMetrics())

  const [menu, setMenu] = useState<{ x: number; y: number } | null>(null)
  const [hasSelection, setHasSelection] = useState(false)
  const menuRef = useRef(menu)
  menuRef.current = menu

  fontSizeRef.current = fontSize
  onFontSizeChangeRef.current = onFontSizeChange
  resolvedThemeRef.current = resolvedTheme
  visibleRef.current = visible

  // Centralized terminal size synchronization with stateful deduplication & automatic retry
  const syncTerminalSize = (): void => {
    if (disposedRef.current || !visibleRef.current) return
    const currentTerm = termRef.current
    const currentFit = fitRef.current
    if (!currentTerm || !currentFit) return

    try {
      currentFit.fit()
    } catch {
      return /* container layout not yet measurable */
    }

    const cols = currentTerm.cols
    const rows = currentTerm.rows
    if (!Number.isInteger(cols) || !Number.isInteger(rows) || cols <= 0 || rows <= 0) {
      return
    }

    const target: TerminalSize = { cols, rows }
    desiredSizeRef.current = target

    // If a request for this exact target is already in flight, wait for its completion.
    if (
      inFlightSizeRef.current &&
      inFlightSizeRef.current.cols === cols &&
      inFlightSizeRef.current.rows === rows
    ) {
      return
    }

    // If another resize is currently in flight, let it finish; its finally block will check desiredSizeRef.
    if (inFlightSizeRef.current) {
      return
    }

    // If target size is already acknowledged by remote PTY, skip duplicate IPC call.
    if (
      acknowledgedSizeRef.current &&
      acknowledgedSizeRef.current.cols === cols &&
      acknowledgedSizeRef.current.rows === rows
    ) {
      return
    }

    inFlightSizeRef.current = target
    metricsRef.current.resizeIpcCount++
    publishMetrics(sessionId, metricsRef.current)
    void Promise.resolve()
      .then(() => {
        if (disposedRef.current || !visibleRef.current) return
        return window.api.sessions.resize(sessionId, cols, rows)
      })
      .then(() => {
        if (disposedRef.current || !visibleRef.current) return
        acknowledgedSizeRef.current = target
        if (retryTimerRef.current != null) {
          window.clearTimeout(retryTimerRef.current)
          retryTimerRef.current = null
        }
      })
      .catch(() => {
        // Keep desiredSizeRef intact on failure; schedule an automatic retry if still visible
        if (disposedRef.current || !visibleRef.current) return
        if (retryTimerRef.current == null) {
          retryTimerRef.current = window.setTimeout(() => {
            retryTimerRef.current = null
            if (!disposedRef.current && visibleRef.current) {
              syncTerminalSize()
            }
          }, RESIZE_RETRY_DELAY_MS)
        }
      })
      .finally(() => {
        if (disposedRef.current) {
          inFlightSizeRef.current = null
          return
        }
        inFlightSizeRef.current = null
        const latest = desiredSizeRef.current
        if (
          latest &&
          visibleRef.current &&
          (latest.cols !== target.cols || latest.rows !== target.rows)
        ) {
          syncTerminalSize()
        }
      })
  }

  const scheduleFit = (immediate = false): void => {
    if (disposedRef.current || !visibleRef.current) return
    if (fitTimerRef.current != null) {
      window.clearTimeout(fitTimerRef.current)
      fitTimerRef.current = null
    }
    if (immediate) {
      requestAnimationFrame(() => {
        if (!disposedRef.current && visibleRef.current) syncTerminalSize()
      })
      return
    }
    fitTimerRef.current = window.setTimeout(() => {
      fitTimerRef.current = null
      requestAnimationFrame(() => {
        if (!disposedRef.current && visibleRef.current) syncTerminalSize()
      })
    }, RESIZE_DEBOUNCE_MS)
  }

  useEffect(() => {
    disposedRef.current = false
    if (!containerRef.current) return

    const term = new Terminal({
      cursorBlink: true,
      fontFamily: buildTerminalFontStack(fontFamily),
      fontSize,
      scrollback: TERMINAL_SCROLLBACK,
      theme: getTerminalTheme(resolvedThemeRef.current),
      allowTransparency: false,
      // UnicodeGraphemesAddon uses xterm's proposed unicode service APIs.
      allowProposedApi: true
    })
    const fit = new FitAddon()
    term.loadAddon(fit)
    try {
      term.loadAddon(new UnicodeGraphemesAddon())
    } catch {
      /* addon is experimental; keep default character widths */
    }
    const metrics = metricsRef.current
    term.open(containerRef.current)
    webglDegradedRef.current = false
    webglRef.current = tryEnableWebglRenderer(term, webglDegradedRef, () => {
      metrics.webglFallbackCount++
      webglRef.current = null
      publishMetrics(sessionId, metrics)
    })
    try {
      fit.fit()
    } catch {
      /* container may be zero-sized on first paint */
    }
    termRef.current = term
    fitRef.current = fit

    // Coalesce high-frequency IPC chunks into one write per animation frame.
    // A frame is capped so a huge pending buffer cannot stall the UI; leftover
    // characters stay queued and are written on the next frame.
    let pending = ''
    let raf: number | null = null
    const flush = (): void => {
      raf = null
      if (disposedRef.current || !pending) return
      const { chunk, rest } = takeWriteChunk(pending, MAX_WRITE_CHARS_PER_FRAME)
      pending = rest
      metrics.flushCount++
      metrics.writeCount++
      metrics.lastFlushBytes = chunk.length
      metrics.totalBytesWritten += chunk.length
      publishMetrics(sessionId, metrics)
      term.write(chunk)
      if (pending && !disposedRef.current) {
        raf = requestAnimationFrame(flush)
      }
    }
    const unsub = registerDataListener(sessionId, (data) => {
      if (disposedRef.current) return
      pending += data
      metrics.dataReceiveCount++
      if (pending.length > metrics.peakPendingBytes) {
        metrics.peakPendingBytes = pending.length
      }
      publishMetrics(sessionId, metrics)
      if (raf == null) raf = requestAnimationFrame(flush)
    })
    const onData = term.onData((data) => {
      if (disposedRef.current) return
      window.api.sessions.write(sessionId, data)
    })

    const ro = new ResizeObserver(() => {
      metrics.resizeObserverCount++
      publishMetrics(sessionId, metrics)
      scheduleFit(false)
    })
    ro.observe(containerRef.current)

    // Initial visible fit
    try {
      scheduleFit(true)
    } catch {
      /* ignore */
    }

    return () => {
      unsub()
      onData.dispose()
      ro.disconnect()
      if (fitTimerRef.current != null) {
        window.clearTimeout(fitTimerRef.current)
        fitTimerRef.current = null
      }
      if (retryTimerRef.current != null) {
        window.clearTimeout(retryTimerRef.current)
        retryTimerRef.current = null
      }
      if (raf != null) {
        cancelAnimationFrame(raf)
        raf = null
      }
      disposedRef.current = true
      inFlightSizeRef.current = null

      // Perform legal pending flush before disposing term instance. Teardown
      // may write the remainder in one go; the live path stays frame-capped.
      if (pending) {
        metrics.totalBytesWritten += pending.length
        metrics.writeCount++
        publishMetrics(sessionId, metrics)
        term.write(pending)
        pending = ''
      }

      if (webglRef.current) {
        try {
          webglRef.current.dispose()
        } catch {
          /* ignore */
        }
        webglRef.current = null
      }

      term.dispose()
      termRef.current = null
      fitRef.current = null
    }
    // Font props are applied via a separate effect so changing font does not rebuild the terminal.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [sessionId, registerDataListener])

  useEffect(() => {
    const term = termRef.current
    if (!term || disposedRef.current) return
    term.options.fontFamily = buildTerminalFontStack(fontFamily)
    term.options.fontSize = fontSize
    if (!visibleRef.current) return
    scheduleFit(true)
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [fontFamily, fontSize, sessionId])

  useEffect(() => {
    const term = termRef.current
    if (!term || disposedRef.current) return
    term.options.theme = getTerminalTheme(resolvedTheme)
  }, [resolvedTheme])

  useEffect(() => {
    if (!visible || disposedRef.current) {
      if (retryTimerRef.current != null) {
        window.clearTimeout(retryTimerRef.current)
        retryTimerRef.current = null
      }
      if (fitTimerRef.current != null) {
        window.clearTimeout(fitTimerRef.current)
        fitTimerRef.current = null
      }
      return
    }
    // Tab became visible: fast fit on next animation frame
    scheduleFit(true)
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [visible, sessionId])

  // Ctrl/Cmd + mouse wheel zooms terminal font size.
  useEffect(() => {
    const el = containerRef.current
    if (!el) return

    const onWheel = (e: WheelEvent): void => {
      if (!e.ctrlKey && !e.metaKey) return
      if (!onFontSizeChangeRef.current) return
      e.preventDefault()
      e.stopPropagation()
      if (e.deltaY === 0) return
      const step = e.deltaY < 0 ? 1 : -1
      onFontSizeChangeRef.current(clampTerminalFontSize(fontSizeRef.current + step))
    }

    el.addEventListener('wheel', onWheel, { passive: false })
    return () => el.removeEventListener('wheel', onWheel)
  }, [])

  // Right-click context menu: Copy, Paste, Clear Screen. The menu closes on
  // an outside mousedown or Escape.
  useEffect(() => {
    const el = containerRef.current
    if (!el) return

    const onContextMenu = (e: MouseEvent): void => {
      e.preventDefault()
      const term = termRef.current
      setHasSelection(term ? term.getSelection().length > 0 : false)
      setMenu({ x: e.clientX, y: e.clientY })
    }

    const onMouseDown = (e: MouseEvent): void => {
      const target = e.target as HTMLElement
      if (target.closest('[data-testid="terminal-context-menu"]')) return
      setMenu(null)
    }

    const onKeyDown = (e: KeyboardEvent): void => {
      // Only swallow Escape while the menu is open; otherwise it must keep
      // bubbling so xterm can forward it to the remote shell (e.g. vi).
      if (e.key !== 'Escape' || !menuRef.current) return
      e.preventDefault()
      e.stopPropagation()
      setMenu(null)
    }

    el.addEventListener('contextmenu', onContextMenu)
    window.addEventListener('mousedown', onMouseDown)
    window.addEventListener('keydown', onKeyDown)
    return () => {
      el.removeEventListener('contextmenu', onContextMenu)
      window.removeEventListener('mousedown', onMouseDown)
      window.removeEventListener('keydown', onKeyDown)
    }
  }, [])

  const handleCopy = (): void => {
    const term = termRef.current
    if (!term || disposedRef.current) return
    const selection = term.getSelection()
    if (!selection) return
    void navigator.clipboard.writeText(selection).catch(() => {
      /* clipboard write denied; keep the selection intact */
      return
    })
    term.clearSelection()
  }

  const handlePaste = (): void => {
    const term = termRef.current
    if (!term || disposedRef.current) return
    if (navigator.clipboard?.readText) {
      void navigator.clipboard
        .readText()
        .then((text) => {
          if (!disposedRef.current) term.paste(text)
        })
        .catch(() => {
          /* clipboard read denied; fall through to execCommand below */
          document.execCommand('paste')
        })
    } else {
      document.execCommand('paste')
    }
  }

  const handleClear = (): void => {
    if (disposedRef.current) return
    termRef.current?.clear()
    termRef.current?.clearSelection()
  }

  return (
    <div
      ref={containerRef}
      className="terminal-view"
      style={{ display: visible ? 'block' : 'none' }}
    >
      {menu && (
        <TerminalContextMenu
          x={menu.x}
          y={menu.y}
          canCopy={hasSelection}
          onCopy={handleCopy}
          onPaste={handlePaste}
          onClear={handleClear}
          onClose={() => setMenu(null)}
        />
      )}
    </div>
  )
}

import { useEffect, useRef, useState } from 'react'
import { FitAddon } from '@xterm/addon-fit'
import { Terminal } from '@xterm/xterm'
import '@xterm/xterm/css/xterm.css'
import type { ResolvedTheme } from '../../../shared/types'
import { TerminalContextMenu } from './TerminalContextMenu'
import {
  buildTerminalFontStack,
  clampTerminalFontSize,
  getTerminalTheme
} from '../terminal-theme'

/** Cap scrollback so the active terminal stays bounded in memory. */
const TERMINAL_SCROLLBACK = 1000

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
  const fontSizeRef = useRef(fontSize)
  const onFontSizeChangeRef = useRef(onFontSizeChange)
  const resolvedThemeRef = useRef(resolvedTheme)
  const visibleRef = useRef(visible)
  const [menu, setMenu] = useState<{ x: number; y: number } | null>(null)
  const [hasSelection, setHasSelection] = useState(false)
  const menuRef = useRef(menu)
  menuRef.current = menu

  fontSizeRef.current = fontSize
  onFontSizeChangeRef.current = onFontSizeChange
  resolvedThemeRef.current = resolvedTheme
  visibleRef.current = visible

  useEffect(() => {
    if (!containerRef.current) return

    const term = new Terminal({
      cursorBlink: true,
      fontFamily: buildTerminalFontStack(fontFamily),
      fontSize,
      scrollback: TERMINAL_SCROLLBACK,
      theme: getTerminalTheme(resolvedThemeRef.current),
      allowTransparency: false
    })
    const fit = new FitAddon()
    term.loadAddon(fit)
    term.open(containerRef.current)
    try {
      fit.fit()
    } catch {
      /* container may be zero-sized on first paint */
    }
    termRef.current = term
    fitRef.current = fit

    // Coalesce high-frequency IPC chunks into one write per animation frame.
    let pending = ''
    let raf: number | null = null
    const flush = (): void => {
      raf = null
      if (!pending) return
      const chunk = pending
      pending = ''
      term.write(chunk)
    }
    const unsub = registerDataListener(sessionId, (data) => {
      pending += data
      if (raf == null) raf = requestAnimationFrame(flush)
    })
    const onData = term.onData((data) => {
      window.api.sessions.write(sessionId, data)
    })

    let fitTimer: number | null = null
    const scheduleFit = (immediate = false): void => {
      if (!visibleRef.current) return
      if (fitTimer != null) window.clearTimeout(fitTimer)
      const run = (): void => {
        fitTimer = null
        if (!visibleRef.current) return
        try {
          fit.fit()
          void window.api.sessions.resize(sessionId, term.cols, term.rows)
        } catch {
          /* ignore fit errors */
        }
      }
      if (immediate) run()
      else fitTimer = window.setTimeout(run, 80)
    }

    const ro = new ResizeObserver(() => {
      scheduleFit(false)
    })
    ro.observe(containerRef.current)

    try {
      scheduleFit(true)
    } catch {
      /* ignore */
    }

    return () => {
      unsub()
      onData.dispose()
      ro.disconnect()
      if (fitTimer != null) window.clearTimeout(fitTimer)
      if (raf != null) cancelAnimationFrame(raf)
      if (pending) term.write(pending)
      term.dispose()
      termRef.current = null
      fitRef.current = null
    }
    // Font props are applied via a separate effect so changing font does not rebuild the terminal.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [sessionId, registerDataListener])

  useEffect(() => {
    const term = termRef.current
    const fit = fitRef.current
    if (!term || !fit) return
    term.options.fontFamily = buildTerminalFontStack(fontFamily)
    term.options.fontSize = fontSize
    if (!visibleRef.current) return
    try {
      fit.fit()
      void window.api.sessions.resize(sessionId, term.cols, term.rows)
    } catch {
      /* ignore */
    }
  }, [fontFamily, fontSize, sessionId])

  useEffect(() => {
    const term = termRef.current
    if (!term) return
    term.options.theme = getTerminalTheme(resolvedTheme)
  }, [resolvedTheme])

  useEffect(() => {
    if (!visible) return
    requestAnimationFrame(() => {
      try {
        fitRef.current?.fit()
        const term = termRef.current
        if (term) {
          void window.api.sessions.resize(sessionId, term.cols, term.rows)
        }
      } catch {
        /* ignore */
      }
    })
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
    if (!term) return
    const selection = term.getSelection()
    if (!selection) return
    void navigator.clipboard
      .writeText(selection)
      .catch(() => {
        /* clipboard write denied; keep the selection intact */
        return
      })
    term.clearSelection()
  }

  const handlePaste = (): void => {
    const term = termRef.current
    if (!term) return
    if (navigator.clipboard?.readText) {
      void navigator.clipboard
        .readText()
        .then((text) => term.paste(text))
        .catch(() => {
          /* clipboard read denied; fall through to execCommand below */
          document.execCommand('paste')
        })
    } else {
      document.execCommand('paste')
    }
  }

  const handleClear = (): void => {
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

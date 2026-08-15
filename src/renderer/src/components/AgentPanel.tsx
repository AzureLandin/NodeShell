import { useCallback, useEffect, useRef, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { FontAwesomeIcon } from '@fortawesome/react-fontawesome'
import { faArrowUp, faEllipsis, faStop, faTerminal } from '@fortawesome/free-solid-svg-icons'
import { parseIpcThrownError } from '../../../shared/ipc-error'
import type { AgentToolEvent } from '../../../shared/types'
import { AgentMarkdown } from './AgentMarkdown'
import { AgentToolCall } from './AgentToolCall'

interface AgentPanelProps {
  activeSessionId: string | null
  activeSessionTitle: string | null
  connected: boolean
  onOpenSettings: () => void
  onHide?: () => void
}

type AgentEntry =
  | { kind: 'user'; id: string; text: string }
  | { kind: 'assistant'; id: string; text: string }
  | { kind: 'tool'; id: string; name: string; summary: string; ok: boolean; detail?: string }
  | { kind: 'notice'; id: string; text: string; tone: 'error' | 'muted' }

/** Transcripts and run flags are keyed by session so switching tabs keeps both. */
type Transcripts = Record<string, AgentEntry[]>
type RunFlags = Record<string, boolean>

let entrySeq = 0
function nextId(): string {
  entrySeq += 1
  return `a${entrySeq}`
}

/**
 * Consecutive tool calls become one Codex-style stack so a burst of bash/sftp
 * work reads as a single action list, not a row of separate pills.
 */
function renderEntries(entries: AgentEntry[]): React.ReactNode[] {
  const nodes: React.ReactNode[] = []
  let i = 0
  while (i < entries.length) {
    const entry = entries[i]
    if (entry.kind === 'tool') {
      const tools: Extract<AgentEntry, { kind: 'tool' }>[] = []
      while (i < entries.length && entries[i].kind === 'tool') {
        tools.push(entries[i] as Extract<AgentEntry, { kind: 'tool' }>)
        i += 1
      }
      nodes.push(
        <div key={tools[0].id} className="agent-tool-stack">
          {tools.map((tool) => (
            <AgentToolCall
              key={tool.id}
              name={tool.name}
              summary={tool.summary}
              ok={tool.ok}
              detail={tool.detail}
            />
          ))}
        </div>
      )
      continue
    }
    if (entry.kind === 'notice') {
      nodes.push(
        <p key={entry.id} className={`agent-notice-line is-${entry.tone}`}>
          {entry.text}
        </p>
      )
    } else {
      nodes.push(
        <div key={entry.id} className={`agent-msg is-${entry.kind}`}>
          {entry.kind === 'user' ? (
            <p className="agent-msg-text">{entry.text}</p>
          ) : (
            <AgentMarkdown text={entry.text} />
          )}
        </div>
      )
    }
    i += 1
  }
  return nodes
}

/**
 * Right-hand assistant bound to the active SSH session. The transcript shown
 * here is the UI's own record: the conversation the model sees lives in the Go
 * service, which drops it when the session closes.
 */
export function AgentPanel({
  activeSessionId,
  activeSessionTitle,
  connected,
  onOpenSettings,
  onHide
}: AgentPanelProps): React.JSX.Element {
  const { t } = useTranslation()
  const [transcripts, setTranscripts] = useState<Transcripts>({})
  const [running, setRunning] = useState<RunFlags>({})
  const [input, setInput] = useState('')
  const [configured, setConfigured] = useState<boolean | null>(null)
  const listRef = useRef<HTMLDivElement | null>(null)
  const inputRef = useRef<HTMLTextAreaElement | null>(null)
  const menuRef = useRef<HTMLDivElement | null>(null)
  const genRef = useRef<Record<string, number>>({})
  const runGenRef = useRef<Record<string, number>>({})
  const [menuOpen, setMenuOpen] = useState(false)

  const eventLive = (sessionId: string): boolean => {
    const gen = genRef.current[sessionId] ?? 0
    const runGen = runGenRef.current[sessionId]
    if (runGen === undefined) return gen === 0
    return runGen === gen
  }

  const append = useCallback((sessionId: string, entry: AgentEntry): void => {
    setTranscripts((prev) => ({ ...prev, [sessionId]: [...(prev[sessionId] ?? []), entry] }))
  }, [])

  const refreshStatus = useCallback(async (): Promise<boolean> => {
    const agent = window.api.agent
    if (!agent) {
      setConfigured(false)
      return false
    }
    try {
      const status = await agent.status()
      setConfigured(status.configured)
      return status.configured
    } catch {
      setConfigured(false)
      return false
    }
  }, [])

  useEffect(() => {
    void refreshStatus()
  }, [refreshStatus])

  useEffect(() => {
    if (!menuOpen) return
    const onDoc = (e: MouseEvent): void => {
      if (!menuRef.current?.contains(e.target as Node)) setMenuOpen(false)
    }
    document.addEventListener('mousedown', onDoc)
    return () => document.removeEventListener('mousedown', onDoc)
  }, [menuOpen])

  useEffect(() => {
    const agent = window.api.agent
    if (!agent) return
    // A text fragment extends the assistant entry still being written; a tool
    // card closes it, so the next fragment starts a new one.
    const offDelta = agent.onDelta(({ sessionId, delta }) => {
      if (!eventLive(sessionId)) return
      setTranscripts((prev) => {
        if (!eventLive(sessionId)) return prev
        const entries = prev[sessionId] ?? []
        const last = entries[entries.length - 1]
        if (last?.kind === 'assistant') {
          const merged = { ...last, text: last.text + delta }
          return { ...prev, [sessionId]: [...entries.slice(0, -1), merged] }
        }
        return { ...prev, [sessionId]: [...entries, { kind: 'assistant', id: nextId(), text: delta }] }
      })
    })
    const offTool = agent.onTool((event: AgentToolEvent) => {
      if (!eventLive(event.sessionId)) return
      append(event.sessionId, {
        kind: 'tool',
        id: nextId(),
        name: event.name,
        summary: event.summary,
        ok: event.ok,
        detail: event.detail
      })
    })
    const offError = agent.onError(({ sessionId, error }) => {
      if (!eventLive(sessionId)) return
      append(sessionId, {
        kind: 'notice',
        id: nextId(),
        tone: 'error',
        text: error.message || t('agent.failed')
      })
    })
    const offDone = agent.onDone(({ sessionId, aborted }) => {
      if (!eventLive(sessionId)) return
      setRunning((prev) => ({ ...prev, [sessionId]: false }))
      if (aborted) {
        append(sessionId, { kind: 'notice', id: nextId(), tone: 'muted', text: t('agent.aborted') })
      }
    })
    return () => {
      offDelta()
      offTool()
      offError()
      offDone()
    }
  }, [append, t])

  const entries = activeSessionId ? (transcripts[activeSessionId] ?? []) : []
  const isRunning = activeSessionId ? Boolean(running[activeSessionId]) : false

  useEffect(() => {
    const el = listRef.current
    if (el) el.scrollTop = el.scrollHeight
  }, [entries, isRunning])

  const usable = Boolean(activeSessionId && connected)

  const fitInput = (): void => {
    const el = inputRef.current
    if (!el) return
    el.style.height = 'auto'
    el.style.height = `${Math.min(el.scrollHeight, 120)}px`
  }

  // The composer stays enabled without a key: the status is only re-resolved on
  // a send, so disabling it here would stay stuck after the user adds a key in
  // settings. An unconfigured send is rejected before any request and reported
  // in the transcript.
  const send = async (): Promise<void> => {
    const text = input.trim()
    const agent = window.api.agent
    if (!text || !activeSessionId || !usable || isRunning || !agent) return
    setInput('')
    requestAnimationFrame(fitInput)
    append(activeSessionId, { kind: 'user', id: nextId(), text })
    runGenRef.current[activeSessionId] = genRef.current[activeSessionId] ?? 0
    setRunning((prev) => ({ ...prev, [activeSessionId]: true }))
    try {
      await agent.prompt(activeSessionId, activeSessionTitle ?? '', text)
    } catch (e) {
      setRunning((prev) => ({ ...prev, [activeSessionId]: false }))
      // A rejection can mean the key was cleared elsewhere, so the configured
      // hint is re-resolved instead of showing a stale composer.
      const stillConfigured = await refreshStatus()
      const parsed = parseIpcThrownError(e)
      append(activeSessionId, {
        kind: 'notice',
        id: nextId(),
        tone: 'error',
        text: stillConfigured ? parsed.message || t('agent.failed') : t('agent.notConfigured')
      })
    }
  }

  const stop = (): void => {
    if (!activeSessionId) return
    void window.api.agent?.abort(activeSessionId)
  }

  const clear = (): void => {
    if (!activeSessionId) return
    genRef.current[activeSessionId] = (genRef.current[activeSessionId] ?? 0) + 1
    delete runGenRef.current[activeSessionId]
    void window.api.agent?.clear(activeSessionId)
    setTranscripts((prev) => ({ ...prev, [activeSessionId]: [] }))
    setRunning((prev) => ({ ...prev, [activeSessionId]: false }))
  }

  const handleKeyDown = (e: React.KeyboardEvent<HTMLTextAreaElement>): void => {
    // Enter sends, Shift+Enter inserts a newline (chat convention).
    if (e.key === 'Enter' && !e.shiftKey && !e.nativeEvent.isComposing) {
      e.preventDefault()
      void send()
    }
  }

  return (
    <div className="agent-panel">
      <div className="agent-head">
        <h2 className="agent-head-title">{t('agent.title')}</h2>
        <div className="agent-head-menu" ref={menuRef}>
          <button
            type="button"
            className="agent-head-action agent-head-more"
            aria-label={t('agent.menu')}
            aria-expanded={menuOpen}
            onClick={() => setMenuOpen((v) => !v)}
          >
            <FontAwesomeIcon icon={faEllipsis} aria-hidden />
          </button>
          {menuOpen ? (
            <div className="agent-head-menu-pop" role="menu">
              <button
                type="button"
                role="menuitem"
                disabled={!activeSessionId || entries.length === 0}
                onClick={() => {
                  clear()
                  setMenuOpen(false)
                }}
              >
                {t('agent.clear')}
              </button>
              {onHide ? (
                <button
                  type="button"
                  role="menuitem"
                  onClick={() => {
                    onHide()
                    setMenuOpen(false)
                  }}
                >
                  {t('agent.hide')}
                </button>
              ) : null}
            </div>
          ) : null}
        </div>
      </div>

      {configured === false && (
        <div className="agent-notice">
          <p>{t('agent.notConfigured')}</p>
          <button type="button" className="btn-secondary btn-sm" onClick={onOpenSettings}>
            {t('settings.open')}
          </button>
        </div>
      )}

      <div className="agent-log" ref={listRef}>
        {entries.length === 0 && (
          <div className="agent-empty">
            <div className="agent-empty-icon" aria-hidden>
              <FontAwesomeIcon icon={faTerminal} />
            </div>
            <p>{!usable ? t('agent.needSession') : t('agent.empty')}</p>
          </div>
        )}
        {renderEntries(entries)}
        {isRunning && (
          <div className="agent-typing" aria-live="polite">
            <span className="agent-typing-dots" aria-hidden>
              <i />
              <i />
              <i />
            </span>
            <span>{t('agent.working')}</span>
          </div>
        )}
      </div>

      <div className="agent-composer">
        <div className="agent-composer-pill">
          <textarea
            ref={inputRef}
            className="agent-input"
            rows={1}
            value={input}
            placeholder={t('agent.placeholder')}
            aria-label={t('agent.title')}
            disabled={!usable}
            onChange={(e) => {
              setInput(e.target.value)
              fitInput()
            }}
            onKeyDown={handleKeyDown}
          />
          {isRunning ? (
            <button
              type="button"
              className="agent-send is-stop"
              aria-label={t('agent.stop')}
              onClick={stop}
            >
              <FontAwesomeIcon icon={faStop} />
            </button>
          ) : (
            <button
              type="button"
              className="agent-send"
              aria-label={t('agent.send')}
              disabled={!usable || input.trim() === ''}
              onClick={() => void send()}
            >
              <FontAwesomeIcon icon={faArrowUp} />
            </button>
          )}
        </div>
      </div>
    </div>
  )
}

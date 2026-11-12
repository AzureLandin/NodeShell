import styles from './sessions.module.css'
import { bindStyles } from '../../ui/class-names'
import { useEffect, useLayoutEffect, useRef, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { FontAwesomeIcon } from '@fortawesome/react-fontawesome'
import { faPlus, faServer, faWandMagicSparkles } from '@fortawesome/free-solid-svg-icons'
import type { ResolvedTheme } from '../../../../shared/types'
import { isPendingSessionId, type UiSession } from './useSessions'
import { TabsRoot, TabsList, TabsTab, TabsPanel } from '../../ui/tabs'
import { SftpPanel } from '../sftp'
import { TerminalView } from './TerminalView'
import { TransferCenter } from '../transfers'

const classes = bindStyles(styles)

interface SessionTabsProps {
  sessions: UiSession[]
  activeSessionId: string | null
  onSelect: (id: string) => void
  onClose: (id: string) => void
  onReconnect: (session: UiSession) => void
  onCancelConnect?: () => void
  registerDataListener: (sessionId: string, cb: (data: string) => void) => () => void
  sftpExpanded: boolean
  onToggleSftp: () => void
  agentOpen: boolean
  onToggleAgent: () => void
  onOpenHosts: () => void
  terminalFontFamily: string
  terminalFontSize: number
  resolvedTheme: ResolvedTheme
  onTerminalFontSizeChange: (size: number) => void
}

function statusClass(status: UiSession['status']): string {
  return `session-status-dot session-status-${status}`
}

export function SessionTabs({
  sessions,
  activeSessionId,
  onSelect,
  onClose,
  onReconnect,
  onCancelConnect,
  registerDataListener,
  sftpExpanded,
  onToggleSftp,
  agentOpen,
  onToggleAgent,
  onOpenHosts,
  terminalFontFamily,
  terminalFontSize,
  resolvedTheme,
  onTerminalFontSizeChange
}: SessionTabsProps): React.JSX.Element {
  const { t } = useTranslation()
  const activeSession = sessions.find((s) => s.sessionId === activeSessionId)
  const sftpConnected = activeSession?.status === 'connected'
  const connecting = activeSession?.status === 'connecting'
  const [showSlowHint, setShowSlowHint] = useState(false)
  const tabRefs = useRef(new Map<string, HTMLButtonElement>())
  const hostsRef = useRef<HTMLButtonElement>(null)
  const closingFocusRef = useRef<{ sessionId: string; control: HTMLElement } | null>(null)

  useLayoutEffect(() => {
    const pending = closingFocusRef.current
    if (!pending || sessions.some((session) => session.sessionId === pending.sessionId)) return
    closingFocusRef.current = null
    // Closing may await IPC. Only recover focus if removal left it on the body;
    // a terminal, dialog or another control the user entered keeps its focus.
    const focused = document.activeElement
    if (focused !== document.body && focused !== pending.control) return
    const next = activeSessionId ? tabRefs.current.get(activeSessionId) : hostsRef.current
    next?.focus()
  }, [sessions, activeSessionId])

  useEffect(() => {
    if (!connecting) {
      setShowSlowHint(false)
      return
    }
    const id = window.setTimeout(() => setShowSlowHint(true), 3000)
    return () => window.clearTimeout(id)
  }, [connecting, activeSession?.sessionId])

  return (
    <TabsRoot
      className={classes('session-tabs')}
      value={activeSessionId}
      onValueChange={(value) => {
        if (typeof value === 'string' && sessions.some((session) => session.sessionId === value)) {
          onSelect(value)
        }
      }}
    >
      <div className={classes('session-tab-bar glass')}>
        <button
          ref={hostsRef}
          type="button"
          className={classes('hosts-launcher')}
          onClick={onOpenHosts}
          title={t('hostsPicker.open')}
        >
          <FontAwesomeIcon icon={faServer} className={classes('hosts-launcher-icon')} aria-hidden />
          <span>{t('hostsPicker.open')}</span>
        </button>

        <TabsList
          className={classes('session-tab-list')}
          aria-label={t('session.tabsLabel')}
          activateOnFocus
        >
          {sessions.map((session) => (
            <div
              key={session.sessionId}
              className={classes(
                `session-tab${session.sessionId === activeSessionId ? ' session-tab-active' : ''}`
              )}
            >
              <TabsTab
                className={classes('session-tab-trigger')}
                value={session.sessionId}
                ref={(element) => {
                  if (element) tabRefs.current.set(session.sessionId, element)
                  else {
                    const previous = tabRefs.current.get(session.sessionId)
                    if (previous && previous === document.activeElement) {
                      closingFocusRef.current = { sessionId: session.sessionId, control: previous }
                    }
                    tabRefs.current.delete(session.sessionId)
                  }
                }}
              >
                <span className={classes(statusClass(session.status))} aria-hidden />
                <span className={classes('session-tab-title')}>{session.title}</span>
              </TabsTab>
              <button
                type="button"
                className={classes('session-tab-close')}
                aria-label={`${t('session.close')} ${session.title}`}
                onClick={(e) => {
                  const row = e.currentTarget.parentElement
                  if (row?.contains(document.activeElement)) {
                    closingFocusRef.current = {
                      sessionId: session.sessionId,
                      control: document.activeElement as HTMLElement
                    }
                  }
                  void onClose(session.sessionId)
                }}
                onKeyDown={(event) => {
                  if (['ArrowLeft', 'ArrowRight', 'Home', 'End'].includes(event.key)) {
                    event.stopPropagation()
                  }
                }}
              >
                ×
              </button>
            </div>
          ))}
        </TabsList>

        <button
          type="button"
          className={classes('session-tab-add')}
          aria-label={t('session.newTab')}
          title={t('session.newTab')}
          onClick={onOpenHosts}
        >
          <FontAwesomeIcon icon={faPlus} aria-hidden />
        </button>

        <div className={classes('session-tab-bar-actions')}>
          <TransferCenter />
          <button
            type="button"
            className={classes('session-agent-toggle')}
            aria-pressed={agentOpen}
            aria-label={agentOpen ? t('agent.hide') : t('agent.show')}
            title={agentOpen ? t('agent.hide') : t('agent.show')}
            onClick={onToggleAgent}
          >
            <FontAwesomeIcon
              icon={faWandMagicSparkles}
              className={classes('session-agent-toggle-icon')}
              aria-hidden
            />
            <span>{t('agent.title')}</span>
          </button>
        </div>
      </div>

      <div className={classes('session-terminal-area')}>
        {sessions.length === 0 ? (
          <p className={classes('main-placeholder')}>{t('session.placeholder')}</p>
        ) : (
          sessions.map((session) => (
            <TabsPanel
              key={session.sessionId}
              value={session.sessionId}
              keepMounted
              className={classes('session-panel')}
            >
              {(session.status === 'disconnected' || session.status === 'error') && (
                <div className={classes('session-banner')} role="alert">
                  <span>
                    {session.status === 'error'
                      ? (session.errorMessage ?? t('session.error'))
                      : t('session.disconnected')}
                  </span>
                  <button
                    type="button"
                    className={classes('btn-primary btn-sm')}
                    onClick={() => onReconnect(session)}
                  >
                    {t('session.reconnect')}
                  </button>
                </div>
              )}

              {session.status === 'connecting' && (
                <div className={classes('session-connecting')} role="status" aria-live="polite">
                  <p className={classes('session-connecting-status')}>
                    {t('auth.connectingStatus', {
                      name: session.title,
                      host: session.remoteHost ?? '—',
                      port: session.remotePort ?? '—'
                    })}
                  </p>
                  {session.sessionId === activeSessionId && showSlowHint && (
                    <p className={classes('session-connecting-hint')}>{t('auth.connectingHint')}</p>
                  )}
                  {onCancelConnect && (
                    <button
                      type="button"
                      className={classes('btn-secondary btn-sm')}
                      onClick={onCancelConnect}
                    >
                      {t('auth.cancelConnect')}
                    </button>
                  )}
                </div>
              )}

              <div
                className={classes('session-terminals')}
                style={{ display: session.status === 'connecting' ? 'none' : undefined }}
              >
                {!isPendingSessionId(session.sessionId) && (
                  <TerminalView
                    sessionId={session.sessionId}
                    registerDataListener={registerDataListener}
                    visible={session.sessionId === activeSessionId && !connecting}
                    fontFamily={terminalFontFamily}
                    fontSize={terminalFontSize}
                    resolvedTheme={resolvedTheme}
                    onFontSizeChange={onTerminalFontSizeChange}
                  />
                )}
              </div>
            </TabsPanel>
          ))
        )}
      </div>

      <SftpPanel
        sessionId={sftpConnected ? activeSessionId : null}
        connected={Boolean(sftpConnected)}
        expanded={sftpExpanded}
        onToggle={onToggleSftp}
      />
    </TabsRoot>
  )
}

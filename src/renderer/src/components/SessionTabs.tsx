import { useEffect, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { FontAwesomeIcon } from '@fortawesome/react-fontawesome'
import { faPlus, faServer, faWandMagicSparkles } from '@fortawesome/free-solid-svg-icons'
import type { ResolvedTheme } from '../../../shared/types'
import type { UiSession } from '../hooks/useSessions'
import { SftpPanel } from './SftpPanel'
import { TerminalView } from './TerminalView'

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

  useEffect(() => {
    if (!connecting) {
      setShowSlowHint(false)
      return
    }
    const id = window.setTimeout(() => setShowSlowHint(true), 3000)
    return () => window.clearTimeout(id)
  }, [connecting, activeSession?.sessionId])

  return (
    <div className="session-tabs">
      <div className="session-tab-bar glass" role="tablist">
        <button
          type="button"
          className="hosts-launcher"
          onClick={onOpenHosts}
          title={t('hostsPicker.open')}
        >
          <FontAwesomeIcon icon={faServer} className="hosts-launcher-icon" aria-hidden />
          <span>{t('hostsPicker.open')}</span>
        </button>

        {sessions.map((session) => (
          <div
            key={session.sessionId}
            className={`session-tab${session.sessionId === activeSessionId ? ' session-tab-active' : ''}`}
            role="tab"
            aria-selected={session.sessionId === activeSessionId}
            onClick={() => onSelect(session.sessionId)}
            onKeyDown={(e) => {
              if (e.key === 'Enter' || e.key === ' ') {
                e.preventDefault()
                onSelect(session.sessionId)
              }
            }}
            tabIndex={0}
          >
            <span className={statusClass(session.status)} aria-hidden />
            <span className="session-tab-title">{session.title}</span>
            <button
              type="button"
              className="session-tab-close"
              aria-label={`${t('session.close')} ${session.title}`}
              onClick={(e) => {
                e.stopPropagation()
                void onClose(session.sessionId)
              }}
            >
              ×
            </button>
          </div>
        ))}

        <button
          type="button"
          className="session-tab-add"
          aria-label={t('session.newTab')}
          title={t('session.newTab')}
          onClick={onOpenHosts}
        >
          <FontAwesomeIcon icon={faPlus} aria-hidden />
        </button>

        <div className="session-tab-bar-actions">
          <button
            type="button"
            className="session-agent-toggle"
            aria-pressed={agentOpen}
            aria-label={agentOpen ? t('agent.hide') : t('agent.show')}
            title={agentOpen ? t('agent.hide') : t('agent.show')}
            onClick={onToggleAgent}
          >
            <FontAwesomeIcon icon={faWandMagicSparkles} className="session-agent-toggle-icon" aria-hidden />
            <span>{t('agent.title')}</span>
          </button>
        </div>
      </div>

      <div className="session-terminal-area">
        {sessions.length === 0 ? (
          <p className="main-placeholder">{t('session.placeholder')}</p>
        ) : connecting && activeSession ? (
          <div className="session-connecting" role="status" aria-live="polite">
            <p className="session-connecting-status">
              {t('auth.connectingStatus', {
                name: activeSession.title,
                host: activeSession.remoteHost ?? '—',
                port: activeSession.remotePort ?? '—'
              })}
            </p>
            {showSlowHint && (
              <p className="session-connecting-hint">{t('auth.connectingHint')}</p>
            )}
            {onCancelConnect && (
              <button type="button" className="btn-secondary btn-sm" onClick={onCancelConnect}>
                {t('auth.cancelConnect')}
              </button>
            )}
          </div>
        ) : (
          <>
            {activeSession &&
              (activeSession.status === 'disconnected' || activeSession.status === 'error') && (
                <div className="session-banner" role="alert">
                  <span>
                    {activeSession.status === 'error'
                      ? (activeSession.errorMessage ?? t('session.error'))
                      : t('session.disconnected')}
                  </span>
                  <button
                    type="button"
                    className="btn-primary btn-sm"
                    onClick={() => onReconnect(activeSession)}
                  >
                    {t('session.reconnect')}
                  </button>
                </div>
              )}

            <div className="session-terminals">
              {activeSession && activeSession.status !== 'connecting' && (
                <TerminalView
                  key={activeSession.sessionId}
                  sessionId={activeSession.sessionId}
                  registerDataListener={registerDataListener}
                  visible
                  fontFamily={terminalFontFamily}
                  fontSize={terminalFontSize}
                  resolvedTheme={resolvedTheme}
                  onFontSizeChange={onTerminalFontSizeChange}
                />
              )}
            </div>
          </>
        )}
      </div>

      <SftpPanel
        sessionId={sftpConnected ? activeSessionId : null}
        connected={Boolean(sftpConnected)}
        expanded={sftpExpanded}
        onToggle={onToggleSftp}
      />
    </div>
  )
}

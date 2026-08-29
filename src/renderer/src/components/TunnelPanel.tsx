import { useCallback, useEffect, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { FontAwesomeIcon } from '@fortawesome/react-fontawesome'
import {
  faArrowsRotate,
  faArrowUpRightFromSquare,
  faCheck,
  faCopy,
  faNetworkWired,
  faRightLeft,
  faSpinner,
  faStop,
  faTriangleExclamation
} from '@fortawesome/free-solid-svg-icons'
import { parseIpcThrownError } from '../../../shared/ipc-error'
import type { Tunnel, TunnelListener } from '../../../shared/types'

interface TunnelPanelProps {
  activeSessionId: string | null
  connected: boolean
}

function listenerKey(bind: string, port: number): string {
  return `${bind}\0${port}`
}

function findTunnel(tunnels: Tunnel[], bind: string, port: number): Tunnel | undefined {
  return tunnels.find((t) => t.remoteAddr === bind && t.remotePort === port)
}

/**
 * Discover remote TCP listeners and open SSH local forwards onto 127.0.0.1.
 */
export function TunnelPanel({
  activeSessionId,
  connected
}: TunnelPanelProps): React.JSX.Element {
  const { t } = useTranslation()
  const [listeners, setListeners] = useState<TunnelListener[]>([])
  const [tunnels, setTunnels] = useState<Tunnel[]>([])
  const [selected, setSelected] = useState<Set<string>>(new Set())
  const [loading, setLoading] = useState(false)
  const [busyKey, setBusyKey] = useState<string | null>(null)
  const [error, setError] = useState<string | null>(null)
  const [copiedKey, setCopiedKey] = useState<string | null>(null)

  const usable = Boolean(activeSessionId && connected)

  const refresh = useCallback(async (): Promise<void> => {
    if (!activeSessionId || !connected) {
      setListeners([])
      setTunnels([])
      setSelected(new Set())
      setError(null)
      return
    }
    setLoading(true)
    setError(null)
    try {
      const [found, live] = await Promise.all([
        window.api.tunnels.discover(activeSessionId),
        window.api.tunnels.list(activeSessionId)
      ])
      setListeners(found)
      setTunnels(live)
      setSelected((prev) => {
        const next = new Set<string>()
        for (const l of found) {
          const key = listenerKey(l.bind, l.port)
          if (prev.has(key)) next.add(key)
        }
        return next
      })
    } catch (e) {
      setListeners([])
      setError(parseIpcThrownError(e).message || t('tunnels.unavailable'))
    } finally {
      setLoading(false)
    }
  }, [activeSessionId, connected, t])

  useEffect(() => {
    void refresh()
  }, [refresh])

  const toggle = (key: string): void => {
    setSelected((prev) => {
      const next = new Set(prev)
      if (next.has(key)) next.delete(key)
      else next.add(key)
      return next
    })
  }

  const forwardOne = async (bind: string, port: number): Promise<void> => {
    if (!activeSessionId) return
    const key = listenerKey(bind, port)
    setBusyKey(key)
    setError(null)
    try {
      const tun = await window.api.tunnels.start(activeSessionId, bind, port)
      setTunnels((prev) => {
        const rest = prev.filter((x) => x.id !== tun.id)
        return [...rest, tun]
      })
    } catch (e) {
      setError(parseIpcThrownError(e).message || t('tunnels.forwardFailed'))
    } finally {
      setBusyKey(null)
    }
  }

  const forwardSelected = async (): Promise<void> => {
    for (const l of listeners) {
      const key = listenerKey(l.bind, l.port)
      if (!selected.has(key)) continue
      if (findTunnel(tunnels, l.bind, l.port)) continue
      await forwardOne(l.bind, l.port)
    }
  }

  const stopOne = async (tun: Tunnel): Promise<void> => {
    if (!activeSessionId) return
    const key = listenerKey(tun.remoteAddr, tun.remotePort)
    setBusyKey(key)
    setError(null)
    try {
      await window.api.tunnels.stop(activeSessionId, tun.id)
      setTunnels((prev) => prev.filter((x) => x.id !== tun.id))
    } catch (e) {
      setError(parseIpcThrownError(e).message || t('tunnels.stopFailed'))
    } finally {
      setBusyKey(null)
    }
  }

  const copyLocal = async (tun: Tunnel): Promise<void> => {
    const text = `${tun.localHost}:${tun.localPort}`
    const key = listenerKey(tun.remoteAddr, tun.remotePort)
    try {
      await navigator.clipboard.writeText(text)
      setCopiedKey(key)
      window.setTimeout(() => {
        setCopiedKey((curr) => (curr === key ? null : curr))
      }, 1500)
    } catch {
      /* clipboard may be denied */
    }
  }

  const openLocal = (tun: Tunnel): void => {
    void window.api.app.openExternal?.(`http://${tun.localHost}:${tun.localPort}`)
  }

  const selectedUnmappedCount = listeners.filter((l) => {
    const key = listenerKey(l.bind, l.port)
    return selected.has(key) && !findTunnel(tunnels, l.bind, l.port)
  }).length

  const activeTunnelCount = listeners.filter((l) =>
    Boolean(findTunnel(tunnels, l.bind, l.port))
  ).length

  return (
    <div className="sidebar-tunnels">
      <div className="tunnel-header-block">
        <div className="tunnel-header-row">
          <div className="tunnel-header-identity">
            <FontAwesomeIcon
              icon={faNetworkWired}
              className="tunnel-header-icon"
              aria-hidden="true"
            />
            <h3 className="tunnel-title">{t('tunnels.headerTitle')}</h3>
          </div>
          <span
            className={`tunnel-session-badge ${usable ? 'is-connected' : 'is-disconnected'}`}
          >
            <span className="tunnel-status-dot" aria-hidden="true" />
            <span>{usable ? t('tunnels.connected') : t('tunnels.disconnected')}</span>
          </span>
        </div>

        <div className="tunnel-toolbar">
          <button
            type="button"
            className="btn-secondary btn-sm"
            disabled={!usable || loading}
            onClick={() => void refresh()}
            aria-label={t('tunnels.refresh')}
            title={t('tunnels.refresh')}
          >
            <FontAwesomeIcon
              icon={faArrowsRotate}
              className={loading ? 'fa-spin' : ''}
              aria-hidden="true"
            />
            <span>{t('tunnels.refresh')}</span>
          </button>
          <button
            type="button"
            className="btn-primary btn-sm"
            disabled={!usable || loading || selectedUnmappedCount === 0 || busyKey != null}
            onClick={() => void forwardSelected()}
            aria-label={t('tunnels.forward')}
          >
            <FontAwesomeIcon icon={faRightLeft} aria-hidden="true" />
            <span>
              {t('tunnels.forward')}
              {selectedUnmappedCount > 0 ? ` (${selectedUnmappedCount})` : ''}
            </span>
          </button>
        </div>

        {usable && listeners.length > 0 && (
          <div className="tunnel-summary">
            <span>{t('tunnels.discoveredCount', { count: listeners.length })}</span>
            <span className="tunnel-summary-divider">·</span>
            <span>{t('tunnels.forwardedCount', { count: activeTunnelCount })}</span>
          </div>
        )}
      </div>

      {!usable && (
        <div className="tunnel-empty">
          <div className="tunnel-empty-icon-wrap" aria-hidden="true">
            <FontAwesomeIcon icon={faNetworkWired} className="tunnel-empty-icon" />
          </div>
          <p className="tunnel-empty-title">{t('tunnels.unconnectedTitle')}</p>
          <p className="tunnel-empty-description">{t('tunnels.unconnectedHint')}</p>
        </div>
      )}

      {usable && loading && listeners.length === 0 && (
        <div className="tunnel-empty tunnel-loading">
          <FontAwesomeIcon
            icon={faSpinner}
            spin
            className="tunnel-loading-spinner"
            aria-hidden="true"
          />
          <p className="tunnel-empty-title">{t('tunnels.loading')}</p>
        </div>
      )}

      {usable && error && (
        <div className="tunnel-empty tunnel-empty-error">
          <div className="tunnel-empty-icon-wrap is-error" aria-hidden="true">
            <FontAwesomeIcon icon={faTriangleExclamation} className="tunnel-empty-icon" />
          </div>
          <p className="tunnel-empty-title">{t('tunnels.errorTitle')}</p>
          <p className="tunnel-empty-description">{error}</p>
          <button
            type="button"
            className="btn-secondary btn-sm"
            onClick={() => void refresh()}
          >
            <FontAwesomeIcon icon={faArrowsRotate} aria-hidden="true" />
            <span>{t('tunnels.retry')}</span>
          </button>
        </div>
      )}

      {usable && !error && !loading && listeners.length === 0 && (
        <div className="tunnel-empty">
          <div className="tunnel-empty-icon-wrap" aria-hidden="true">
            <FontAwesomeIcon icon={faNetworkWired} className="tunnel-empty-icon" />
          </div>
          <p className="tunnel-empty-title">{t('tunnels.emptyTitle')}</p>
          <p className="tunnel-empty-description">{t('tunnels.emptyHint')}</p>
        </div>
      )}

      {usable && listeners.length > 0 && (
        <ul className="tunnel-list">
          {listeners.map((l) => {
            const key = listenerKey(l.bind, l.port)
            const tun = findTunnel(tunnels, l.bind, l.port)
            const busy = busyKey === key
            const isCopied = copiedKey === key

            return (
              <li key={key} className={`tunnel-row${tun ? ' is-mapped' : ''}`}>
                <div className="tunnel-row-header">
                  <label className="tunnel-pick">
                    <input
                      type="checkbox"
                      checked={selected.has(key)}
                      disabled={busy}
                      onChange={() => toggle(key)}
                      aria-label={t('tunnels.portLabel', { bind: l.bind, port: l.port })}
                    />
                    <span className="tunnel-port">{l.port}</span>
                    <span className="tunnel-bind" title={l.bind}>
                      {l.bind}
                    </span>
                  </label>
                  <span
                    className={`tunnel-state-tag ${busy ? 'is-busy' : tun ? 'is-mapped' : 'is-unmapped'}`}
                  >
                    {busy
                      ? t('tunnels.statusMapping')
                      : tun
                        ? t('tunnels.statusMapped')
                        : t('tunnels.statusUnmapped')}
                  </span>
                </div>

                {tun ? (
                  <div className="tunnel-live">
                    <span
                      className="tunnel-local"
                      title={`${tun.localHost}:${tun.localPort}`}
                    >
                      {tun.localHost}:{tun.localPort}
                    </span>
                    <div className="tunnel-item-actions">
                      <button
                        type="button"
                        className="btn-secondary btn-sm"
                        onClick={() => void copyLocal(tun)}
                        title={t('tunnels.copy')}
                        aria-label={t('tunnels.copy')}
                      >
                        <FontAwesomeIcon
                          icon={isCopied ? faCheck : faCopy}
                          aria-hidden="true"
                        />
                        <span>{isCopied ? t('tunnels.copied') : t('tunnels.copy')}</span>
                      </button>
                      <button
                        type="button"
                        className="btn-secondary btn-sm"
                        onClick={() => openLocal(tun)}
                        title={t('tunnels.open')}
                        aria-label={t('tunnels.open')}
                      >
                        <FontAwesomeIcon
                          icon={faArrowUpRightFromSquare}
                          aria-hidden="true"
                        />
                        <span>{t('tunnels.open')}</span>
                      </button>
                      <button
                        type="button"
                        className="btn-danger btn-sm"
                        disabled={busy}
                        onClick={() => void stopOne(tun)}
                        title={t('tunnels.stop')}
                        aria-label={t('tunnels.stop')}
                      >
                        <FontAwesomeIcon icon={faStop} aria-hidden="true" />
                        <span>{t('tunnels.stop')}</span>
                      </button>
                    </div>
                  </div>
                ) : (
                  <div className="tunnel-unmapped-action">
                    <button
                      type="button"
                      className="btn-secondary btn-sm"
                      disabled={busy}
                      onClick={() => void forwardOne(l.bind, l.port)}
                      title={t('tunnels.map')}
                    >
                      <FontAwesomeIcon
                        icon={busy ? faSpinner : faRightLeft}
                        className={busy ? 'fa-spin' : ''}
                        aria-hidden="true"
                      />
                      <span>{t('tunnels.map')}</span>
                    </button>
                  </div>
                )}
              </li>
            )
          })}
        </ul>
      )}
    </div>
  )
}

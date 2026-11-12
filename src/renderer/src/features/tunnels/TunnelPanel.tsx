import styles from './tunnels.module.css'
import { bindStyles } from '../../ui/class-names'
import { useCallback, useEffect, useLayoutEffect, useRef, useState } from 'react'
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
import { parseIpcThrownError } from '../../../../shared/ipc-error'
import type { Tunnel, TunnelListener } from '../../../../shared/types'

const classes = bindStyles(styles)

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
export function TunnelPanel({ activeSessionId, connected }: TunnelPanelProps): React.JSX.Element {
  const { t } = useTranslation()
  const [listeners, setListeners] = useState<TunnelListener[]>([])
  const [tunnels, setTunnels] = useState<Tunnel[]>([])
  const [selected, setSelected] = useState<Set<string>>(new Set())
  const [loading, setLoading] = useState(false)
  const [busyKey, setBusyKey] = useState<string | null>(null)
  const [error, setError] = useState<string | null>(null)
  const [copiedKey, setCopiedKey] = useState<string | null>(null)
  const lifetime = useRef(0)
  const currentSession = useRef<string | null>(null)
  const readRevision = useRef(0)
  const commandPending = useRef(false)
  const copyTimer = useRef<ReturnType<typeof setTimeout> | null>(null)

  const isCurrent = useCallback(
    (id: string, generation: number): boolean =>
      currentSession.current === id && lifetime.current === generation,
    []
  )

  useLayoutEffect(() => {
    lifetime.current += 1
    currentSession.current = connected ? activeSessionId : null
    commandPending.current = false
    setListeners([])
    setTunnels([])
    setSelected(new Set())
    setLoading(false)
    setBusyKey(null)
    setError(null)
    setCopiedKey(null)
    return () => {
      lifetime.current += 1
      readRevision.current += 1
      currentSession.current = null
      if (copyTimer.current !== null) clearTimeout(copyTimer.current)
    }
  }, [activeSessionId, connected])

  const usable = Boolean(activeSessionId && connected)

  const refresh = useCallback(async (): Promise<void> => {
    const generation = lifetime.current
    if (
      !activeSessionId ||
      !connected ||
      !isCurrent(activeSessionId, generation) ||
      commandPending.current
    ) {
      return
    }
    const revision = ++readRevision.current
    setLoading(true)
    setError(null)
    try {
      const [found, live] = await Promise.all([
        window.api.tunnels.discover(activeSessionId),
        window.api.tunnels.list(activeSessionId)
      ])
      if (!isCurrent(activeSessionId, generation) || revision !== readRevision.current) return
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
      if (!isCurrent(activeSessionId, generation) || revision !== readRevision.current) return
      setListeners([])
      setError(parseIpcThrownError(e).message || t('tunnels.unavailable'))
    } finally {
      if (isCurrent(activeSessionId, generation) && revision === readRevision.current)
        setLoading(false)
    }
  }, [activeSessionId, connected, t, isCurrent])

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

  const forward = async (targets: TunnelListener[]): Promise<void> => {
    const generation = lifetime.current
    if (!activeSessionId || !isCurrent(activeSessionId, generation) || commandPending.current)
      return
    commandPending.current = true
    readRevision.current += 1
    setLoading(false)
    setError(null)
    try {
      for (const { bind, port } of targets) {
        if (!isCurrent(activeSessionId, generation)) return
        setBusyKey(listenerKey(bind, port))
        try {
          const tun = await window.api.tunnels.start(activeSessionId, bind, port)
          if (!isCurrent(activeSessionId, generation)) return
          setTunnels((prev) => [...prev.filter((item) => item.id !== tun.id), tun])
        } catch (e) {
          if (!isCurrent(activeSessionId, generation)) return
          setError(parseIpcThrownError(e).message || t('tunnels.forwardFailed'))
        }
      }
    } finally {
      if (isCurrent(activeSessionId, generation)) {
        commandPending.current = false
        setBusyKey(null)
      }
    }
  }

  const forwardOne = (bind: string, port: number): Promise<void> => forward([{ bind, port }])

  const forwardSelected = (): Promise<void> =>
    forward(
      listeners.filter(
        (item) =>
          selected.has(listenerKey(item.bind, item.port)) &&
          !findTunnel(tunnels, item.bind, item.port)
      )
    )

  const stopOne = async (tun: Tunnel): Promise<void> => {
    const generation = lifetime.current
    if (!activeSessionId || !isCurrent(activeSessionId, generation) || commandPending.current)
      return
    commandPending.current = true
    readRevision.current += 1
    setLoading(false)
    const key = listenerKey(tun.remoteAddr, tun.remotePort)
    setBusyKey(key)
    setError(null)
    try {
      await window.api.tunnels.stop(activeSessionId, tun.id)
      if (!isCurrent(activeSessionId, generation)) return
      setTunnels((prev) => prev.filter((x) => x.id !== tun.id))
    } catch (e) {
      if (isCurrent(activeSessionId, generation))
        setError(parseIpcThrownError(e).message || t('tunnels.stopFailed'))
    } finally {
      if (isCurrent(activeSessionId, generation)) {
        commandPending.current = false
        setBusyKey(null)
      }
    }
  }

  const copyLocal = async (tun: Tunnel): Promise<void> => {
    const generation = lifetime.current
    const text = `${tun.localHost}:${tun.localPort}`
    const key = listenerKey(tun.remoteAddr, tun.remotePort)
    try {
      await navigator.clipboard.writeText(text)
      if (!isCurrent(tun.sessionId, generation)) return
      setCopiedKey(key)
      if (copyTimer.current !== null) clearTimeout(copyTimer.current)
      copyTimer.current = setTimeout(() => {
        copyTimer.current = null
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
    <div className={classes('sidebar-tunnels')}>
      <div className={classes('tunnel-header-block')}>
        <div className={classes('tunnel-header-row')}>
          <div className={classes('tunnel-header-identity')}>
            <FontAwesomeIcon
              icon={faNetworkWired}
              className={classes('tunnel-header-icon')}
              aria-hidden="true"
            />
            <h3 className={classes('tunnel-title')}>{t('tunnels.headerTitle')}</h3>
          </div>
          <span
            className={classes(
              `tunnel-session-badge ${usable ? 'is-connected' : 'is-disconnected'}`
            )}
          >
            <span className={classes('tunnel-status-dot')} aria-hidden="true" />
            <span>{usable ? t('tunnels.connected') : t('tunnels.disconnected')}</span>
          </span>
        </div>

        <div className={classes('tunnel-toolbar')}>
          <button
            type="button"
            className={classes('btn-secondary btn-sm')}
            disabled={!usable || loading || busyKey !== null}
            onClick={() => void refresh()}
            aria-label={t('tunnels.refresh')}
            title={t('tunnels.refresh')}
          >
            <FontAwesomeIcon
              icon={faArrowsRotate}
              className={classes(loading ? 'fa-spin' : '')}
              aria-hidden="true"
            />
            <span>{t('tunnels.refresh')}</span>
          </button>
          <button
            type="button"
            className={classes('btn-primary btn-sm')}
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
          <div className={classes('tunnel-summary')}>
            <span>{t('tunnels.discoveredCount', { count: listeners.length })}</span>
            <span className={classes('tunnel-summary-divider')}>·</span>
            <span>{t('tunnels.forwardedCount', { count: activeTunnelCount })}</span>
          </div>
        )}
      </div>

      {!usable && (
        <div className={classes('tunnel-empty')}>
          <div className={classes('tunnel-empty-icon-wrap')} aria-hidden="true">
            <FontAwesomeIcon icon={faNetworkWired} className={classes('tunnel-empty-icon')} />
          </div>
          <p className={classes('tunnel-empty-title')}>{t('tunnels.unconnectedTitle')}</p>
          <p className={classes('tunnel-empty-description')}>{t('tunnels.unconnectedHint')}</p>
        </div>
      )}

      {usable && loading && listeners.length === 0 && (
        <div className={classes('tunnel-empty tunnel-loading')}>
          <FontAwesomeIcon
            icon={faSpinner}
            spin
            className={classes('tunnel-loading-spinner')}
            aria-hidden="true"
          />
          <p className={classes('tunnel-empty-title')}>{t('tunnels.loading')}</p>
        </div>
      )}

      {usable && error && (
        <div className={classes('tunnel-empty tunnel-empty-error')}>
          <div className={classes('tunnel-empty-icon-wrap is-error')} aria-hidden="true">
            <FontAwesomeIcon
              icon={faTriangleExclamation}
              className={classes('tunnel-empty-icon')}
            />
          </div>
          <p className={classes('tunnel-empty-title')}>{t('tunnels.errorTitle')}</p>
          <p className={classes('tunnel-empty-description')}>{error}</p>
          <button
            type="button"
            className={classes('btn-secondary btn-sm')}
            disabled={loading || busyKey !== null}
            onClick={() => void refresh()}
          >
            <FontAwesomeIcon icon={faArrowsRotate} aria-hidden="true" />
            <span>{t('tunnels.retry')}</span>
          </button>
        </div>
      )}

      {usable && !error && !loading && listeners.length === 0 && (
        <div className={classes('tunnel-empty')}>
          <div className={classes('tunnel-empty-icon-wrap')} aria-hidden="true">
            <FontAwesomeIcon icon={faNetworkWired} className={classes('tunnel-empty-icon')} />
          </div>
          <p className={classes('tunnel-empty-title')}>{t('tunnels.emptyTitle')}</p>
          <p className={classes('tunnel-empty-description')}>{t('tunnels.emptyHint')}</p>
        </div>
      )}

      {usable && listeners.length > 0 && (
        <ul className={classes('tunnel-list')}>
          {listeners.map((l) => {
            const key = listenerKey(l.bind, l.port)
            const tun = findTunnel(tunnels, l.bind, l.port)
            const busy = busyKey === key
            const isCopied = copiedKey === key

            return (
              <li key={key} className={classes(`tunnel-row${tun ? ' is-mapped' : ''}`)}>
                <div className={classes('tunnel-row-header')}>
                  <label className={classes('tunnel-pick')}>
                    <input
                      type="checkbox"
                      checked={selected.has(key)}
                      disabled={loading || busyKey !== null}
                      onChange={() => toggle(key)}
                      aria-label={t('tunnels.portLabel', { bind: l.bind, port: l.port })}
                    />
                    <span className={classes('tunnel-port')}>{l.port}</span>
                    <span className={classes('tunnel-bind')} title={l.bind}>
                      {l.bind}
                    </span>
                  </label>
                  <span
                    className={classes(
                      `tunnel-state-tag ${busy ? 'is-busy' : tun ? 'is-mapped' : 'is-unmapped'}`
                    )}
                  >
                    {busy
                      ? t('tunnels.statusMapping')
                      : tun
                        ? t('tunnels.statusMapped')
                        : t('tunnels.statusUnmapped')}
                  </span>
                </div>

                {tun ? (
                  <div className={classes('tunnel-live')}>
                    <span
                      className={classes('tunnel-local')}
                      title={`${tun.localHost}:${tun.localPort}`}
                    >
                      {tun.localHost}:{tun.localPort}
                    </span>
                    <div className={classes('tunnel-item-actions')}>
                      <button
                        type="button"
                        className={classes('btn-secondary btn-sm')}
                        onClick={() => void copyLocal(tun)}
                        title={t('tunnels.copy')}
                        aria-label={t('tunnels.copy')}
                      >
                        <FontAwesomeIcon icon={isCopied ? faCheck : faCopy} aria-hidden="true" />
                        <span>{isCopied ? t('tunnels.copied') : t('tunnels.copy')}</span>
                      </button>
                      <button
                        type="button"
                        className={classes('btn-secondary btn-sm')}
                        onClick={() => openLocal(tun)}
                        title={t('tunnels.open')}
                        aria-label={t('tunnels.open')}
                      >
                        <FontAwesomeIcon icon={faArrowUpRightFromSquare} aria-hidden="true" />
                        <span>{t('tunnels.open')}</span>
                      </button>
                      <button
                        type="button"
                        className={classes('btn-danger btn-sm')}
                        disabled={loading || busyKey !== null}
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
                  <div className={classes('tunnel-unmapped-action')}>
                    <button
                      type="button"
                      className={classes('btn-secondary btn-sm')}
                      disabled={loading || busyKey !== null}
                      onClick={() => void forwardOne(l.bind, l.port)}
                      title={t('tunnels.map')}
                    >
                      <FontAwesomeIcon
                        icon={busy ? faSpinner : faRightLeft}
                        className={classes(busy ? 'fa-spin' : '')}
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

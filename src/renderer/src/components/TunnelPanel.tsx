import { useCallback, useEffect, useState } from 'react'
import { useTranslation } from 'react-i18next'
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
    try {
      await navigator.clipboard.writeText(text)
    } catch {
      /* clipboard may be denied */
    }
  }

  const openLocal = (tun: Tunnel): void => {
    void window.api.app.openExternal?.(`http://${tun.localHost}:${tun.localPort}`)
  }

  const selectedUnmapped = listeners.some((l) => {
    const key = listenerKey(l.bind, l.port)
    return selected.has(key) && !findTunnel(tunnels, l.bind, l.port)
  })

  return (
    <div className="sidebar-tunnels">
      <div className="tunnel-toolbar">
        <button
          type="button"
          className="btn-secondary btn-sm"
          disabled={!usable || loading}
          onClick={() => void refresh()}
        >
          {t('tunnels.refresh')}
        </button>
        <button
          type="button"
          className="btn-primary btn-sm"
          disabled={!usable || loading || !selectedUnmapped || busyKey != null}
          onClick={() => void forwardSelected()}
        >
          {t('tunnels.forward')}
        </button>
      </div>

      {!usable && <p className="tunnel-hint">{t('tunnels.needSession')}</p>}
      {usable && error && <p className="tunnel-error">{error}</p>}
      {usable && !error && !loading && listeners.length === 0 && (
        <p className="tunnel-hint">{t('tunnels.empty')}</p>
      )}

      {usable && listeners.length > 0 && (
        <ul className="tunnel-list">
          {listeners.map((l) => {
            const key = listenerKey(l.bind, l.port)
            const tun = findTunnel(tunnels, l.bind, l.port)
            const busy = busyKey === key
            return (
              <li key={key} className="tunnel-row">
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
                {tun ? (
                  <div className="tunnel-live">
                    <span className="tunnel-local">
                      {tun.localHost}:{tun.localPort}
                    </span>
                    <button
                      type="button"
                      className="btn-secondary btn-sm"
                      disabled={busy}
                      onClick={() => void stopOne(tun)}
                    >
                      {t('tunnels.stop')}
                    </button>
                    <button
                      type="button"
                      className="btn-secondary btn-sm"
                      onClick={() => void copyLocal(tun)}
                    >
                      {t('tunnels.copy')}
                    </button>
                    <button
                      type="button"
                      className="btn-secondary btn-sm"
                      onClick={() => openLocal(tun)}
                    >
                      {t('tunnels.open')}
                    </button>
                  </div>
                ) : (
                  <button
                    type="button"
                    className="btn-secondary btn-sm"
                    disabled={busy}
                    onClick={() => void forwardOne(l.bind, l.port)}
                  >
                    {t('tunnels.map')}
                  </button>
                )}
              </li>
            )
          })}
        </ul>
      )}
    </div>
  )
}

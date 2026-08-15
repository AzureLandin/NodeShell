import { useEffect, useMemo, useState } from 'react'
import { useTranslation } from 'react-i18next'
import type { MonitorSnapshot } from '../../../shared/types'

interface SidebarPanelProps {
  activeSessionId: string | null
  activeSessionTitle: string | null
  connected: boolean
}

interface NetSample {
  rx: number
  tx: number
}

const NET_HISTORY = 36
const CHART_W = 180
const CHART_H = 72
const LABEL_W = 28

function formatBytes(bytes: number): string {
  const n = Number(bytes)
  if (!Number.isFinite(n) || n < 0) return '0B'
  if (n < 1024) return `${Math.round(n)}B`
  if (n < 1024 * 1024) return `${(n / 1024).toFixed(n >= 100 * 1024 ? 0 : 1)}K`
  if (n < 1024 * 1024 * 1024) {
    const mb = n / (1024 * 1024)
    return `${mb >= 100 ? mb.toFixed(0) : mb.toFixed(1)}M`
  }
  const gb = n / (1024 * 1024 * 1024)
  return `${gb.toFixed(1)}G`
}

function formatRate(bps: number | null): string {
  if (bps === null) return '—'
  return `${formatBytes(bps)}/s`
}

function formatAxis(bytes: number): string {
  return formatBytes(bytes)
}

function niceCeiling(value: number): number {
  if (!Number.isFinite(value) || value <= 0) return 3
  const exp = Math.pow(10, Math.floor(Math.log10(value)))
  if (!Number.isFinite(exp) || exp <= 0) return 3
  const scaled = value / exp
  const nice = scaled <= 1 ? 1 : scaled <= 2 ? 2 : scaled <= 3 ? 3 : scaled <= 5 ? 5 : 10
  return nice * exp
}

function percent(used: number, total: number): number {
  if (total <= 0) return 0
  return Math.min(100, Math.max(0, (used / total) * 100))
}

function toPolyline(values: number[], max: number, width: number, height: number): string {
  if (values.length === 0 || max <= 0) return ''
  const n = Math.max(values.length - 1, 1)
  return values
    .map((v, i) => {
      const x = (i / n) * width
      const y = height - (Math.min(v, max) / max) * height
      return `${x.toFixed(1)},${y.toFixed(1)}`
    })
    .join(' ')
}

function MetricBar({
  label,
  percentText,
  detailText,
  ratio,
  tone
}: {
  label: string
  percentText: string
  detailText?: string
  ratio: number
  tone: 'cpu' | 'mem' | 'swap'
}): React.JSX.Element {
  return (
    <div className="monitor-metric">
      <div className="monitor-metric-label">{label}</div>
      <div className={`monitor-bar monitor-bar-${tone}`}>
        <div
          className="monitor-bar-fill"
          style={{ width: `${Math.min(100, Math.max(0, ratio))}%` }}
        />
        <div className="monitor-bar-text">
          <span>{percentText}</span>
          {detailText ? <span>{detailText}</span> : <span />}
        </div>
      </div>
    </div>
  )
}

function NetProbe({
  rxBps,
  txBps,
  history
}: {
  rxBps: number | null
  txBps: number | null
  history: NetSample[]
}): React.JSX.Element {
  const { t } = useTranslation()

  const maxRate = useMemo(() => {
    let peak = 0
    for (const s of history) {
      peak = Math.max(peak, s.rx, s.tx)
    }
    if (rxBps != null) peak = Math.max(peak, rxBps)
    if (txBps != null) peak = Math.max(peak, txBps)
    return niceCeiling(peak || 3)
  }, [history, rxBps, txBps])

  const yLabels = [maxRate, (maxRate * 2) / 3, maxRate / 3]
  const plotW = CHART_W - LABEL_W - 4
  const txLine = toPolyline(
    history.map((s) => s.tx),
    maxRate,
    plotW,
    CHART_H
  )
  const rxLine = toPolyline(
    history.map((s) => s.rx),
    maxRate,
    plotW,
    CHART_H
  )

  return (
    <div className="net-probe">
      <div className="net-probe-header">
        <span className="net-probe-dir net-probe-up" title={t('monitor.netUp')}>
          <span className="net-probe-arrow" aria-hidden>
            ↑
          </span>
          <span>{formatRate(txBps)}</span>
        </span>
        <span className="net-probe-dir net-probe-down" title={t('monitor.netDown')}>
          <span className="net-probe-arrow" aria-hidden>
            ↓
          </span>
          <span>{formatRate(rxBps)}</span>
        </span>
      </div>

      <div className="net-probe-chart" style={{ height: CHART_H }}>
        <div className="net-probe-ylabels" style={{ width: LABEL_W }}>
          {yLabels.map((v) => (
            <span key={v}>{formatAxis(v)}</span>
          ))}
        </div>
        <svg
          className="net-probe-svg"
          viewBox={`0 0 ${plotW} ${CHART_H}`}
          width={plotW}
          height={CHART_H}
          preserveAspectRatio="none"
        >
          {[0.0, 1 / 3, 2 / 3, 1].map((p) => {
            const y = CHART_H * (1 - p)
            return (
              <line
                key={p}
                x1={0}
                y1={y}
                x2={plotW}
                y2={y}
                className="net-probe-grid"
              />
            )
          })}
          {txLine && (
            <polyline points={txLine} className="net-probe-line net-probe-line-up" fill="none" />
          )}
          {rxLine && (
            <polyline points={rxLine} className="net-probe-line net-probe-line-down" fill="none" />
          )}
        </svg>
      </div>
    </div>
  )
}

function MonitorPanel({
  activeSessionId,
  activeSessionTitle,
  connected
}: Pick<SidebarPanelProps, 'activeSessionId' | 'activeSessionTitle' | 'connected'>): React.JSX.Element {
  const { t } = useTranslation()
  const [snapshot, setSnapshot] = useState<MonitorSnapshot | null>(null)
  const [error, setError] = useState<string | null>(null)
  const [netHistory, setNetHistory] = useState<NetSample[]>([])

  useEffect(() => {
    const monitor = window.api.monitor
    if (!monitor) return
    return monitor.onUpdate((event) => {
      if (event.sessionId !== activeSessionId) {
        if (event.sessionId === null && !activeSessionId) {
          setSnapshot(null)
          setError(null)
          setNetHistory([])
        }
        return
      }
      setSnapshot(event.snapshot)
      setError(event.error ?? null)
      if (
        event.snapshot &&
        event.snapshot.netRxBps !== null &&
        event.snapshot.netTxBps !== null &&
        Number.isFinite(event.snapshot.netRxBps) &&
        Number.isFinite(event.snapshot.netTxBps)
      ) {
        setNetHistory((prev) => {
          const next = [
            ...prev,
            { rx: event.snapshot!.netRxBps!, tx: event.snapshot!.netTxBps! }
          ]
          return next.length > NET_HISTORY ? next.slice(-NET_HISTORY) : next
        })
      }
    })
  }, [activeSessionId])

  useEffect(() => {
    const monitor = window.api.monitor
    if (!monitor) return
    if (activeSessionId && connected) {
      const timer = window.setTimeout(() => {
        void monitor.setActive(activeSessionId, activeSessionTitle ?? '')
      }, 400)
      return () => {
        window.clearTimeout(timer)
        void monitor.setActive(null)
      }
    }
    void monitor.setActive(null)
    setSnapshot(null)
    setError(null)
    setNetHistory([])
    return undefined
  }, [activeSessionId, activeSessionTitle, connected])

  const showLive = Boolean(activeSessionId && connected)
  const hostLabel = snapshot?.title || activeSessionTitle || '—'
  const cpuText =
    snapshot?.cpuPercent == null ? '—' : `${snapshot.cpuPercent.toFixed(0)}%`
  const memPct = snapshot ? percent(snapshot.memUsedBytes, snapshot.memTotalBytes) : 0
  const swapPct =
    snapshot && snapshot.swapTotalBytes > 0
      ? percent(snapshot.swapUsedBytes, snapshot.swapTotalBytes)
      : 0

  return (
    <div className="sidebar-monitor">
      <h2 className="sidebar-placeholder-title">{t('monitor.title')}</h2>

      {showLive && error && !snapshot && (
        <p className="monitor-error">{error || t('monitor.unavailable')}</p>
      )}

      <div className={`monitor-body${!snapshot ? ' monitor-body-idle' : ''}`}>
        <p className="monitor-host-chip" title={hostLabel === '—' ? undefined : hostLabel}>
          <span
            className={`session-status-dot ${connected ? 'session-status-connected' : 'session-status-disconnected'}`}
            aria-hidden
          />
          <span className="monitor-host-chip-label">{hostLabel}</span>
        </p>

        <MetricBar
          label={t('monitor.cpu')}
          percentText={cpuText}
          ratio={snapshot?.cpuPercent ?? 0}
          tone="cpu"
        />

        <MetricBar
          label={t('monitor.memory')}
          percentText={!snapshot ? '—' : `${memPct.toFixed(0)}%`}
          detailText={
            !snapshot
              ? '—/—'
              : `${formatBytes(snapshot.memUsedBytes)}/${formatBytes(snapshot.memTotalBytes)}`
          }
          ratio={memPct}
          tone="mem"
        />

        <MetricBar
          label={t('monitor.swap')}
          percentText={
            !snapshot ? '—' : snapshot.swapTotalBytes <= 0 ? '0%' : `${swapPct.toFixed(0)}%`
          }
          detailText={
            !snapshot
              ? '—/—'
              : snapshot.swapTotalBytes <= 0
                ? t('monitor.noSwap')
                : `${formatBytes(snapshot.swapUsedBytes)}/${formatBytes(snapshot.swapTotalBytes)}`
          }
          ratio={swapPct}
          tone="swap"
        />

        <div className="monitor-load">
          <span>{t('monitor.load')}</span>
          <span>
            {!snapshot
              ? '— / — / —'
              : `${snapshot.load1.toFixed(2)} / ${snapshot.load5.toFixed(2)} / ${snapshot.load15.toFixed(2)}`}
          </span>
        </div>

        <div className="monitor-proc">
          <div className="monitor-proc-head">
            <span>{t('monitor.colMem')}</span>
            <span className="monitor-proc-cpu">{t('monitor.colCpu')}</span>
            <span>{t('monitor.colCmd')}</span>
          </div>
          <ul className="monitor-proc-list">
            {(snapshot?.processes ?? []).length === 0 ? (
              <li className="monitor-proc-empty">{!snapshot ? '—' : t('monitor.procEmpty')}</li>
            ) : (
              (snapshot?.processes ?? []).slice(0, 5).map((p, i) => (
                <li key={`${p.command}-${i}`} className="monitor-proc-row">
                  <span>{formatBytes(p.memBytes ?? 0)}</span>
                  <span className="monitor-proc-cpu">{Number(p.cpuPercent ?? 0).toFixed(1)}</span>
                  <span className="monitor-proc-cmd" title={p.command}>
                    {p.command}
                  </span>
                </li>
              ))
            )}
          </ul>
        </div>

        <NetProbe
          rxBps={snapshot?.netRxBps ?? null}
          txBps={snapshot?.netTxBps ?? null}
          history={netHistory}
        />

        {showLive && error && snapshot && <p className="monitor-error">{error}</p>}
      </div>
    </div>
  )
}

/**
 * Left sidebar: host monitor. Settings live on the icon rail; the agent lives
 * in the right-hand dock.
 */
export function SidebarPanel({
  activeSessionId,
  activeSessionTitle,
  connected
}: SidebarPanelProps): React.JSX.Element {
  return (
    <div className="sidebar-panel">
      <MonitorPanel
        activeSessionId={activeSessionId}
        activeSessionTitle={activeSessionTitle}
        connected={connected}
      />
    </div>
  )
}

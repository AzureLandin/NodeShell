import { useCallback, useEffect, useRef, useState } from 'react'
import { createPortal } from 'react-dom'
import { useTranslation } from 'react-i18next'
import { FontAwesomeIcon } from '@fortawesome/react-fontawesome'
import {
  faArrowDown,
  faArrowUp,
  faBan,
  faCheck,
  faRightLeft,
  faRotateRight,
  faSpinner,
  faTrashCan,
  faXmark
} from '@fortawesome/free-solid-svg-icons'
import {
  formatBytes,
  formatEta,
  formatSpeed,
  useTransferTasks,
  type TransferTaskWithMetrics
} from '../hooks/useTransferTasks'

export function TransferCenter(): React.JSX.Element {
  const { t } = useTranslation()
  const [isOpen, setIsOpen] = useState(false)
  const [isHovered, setIsHovered] = useState(false)
  const [popoverStyle, setPopoverStyle] = useState<React.CSSProperties>({})
  const panelRef = useRef<HTMLDivElement>(null)
  const triggerRef = useRef<HTMLButtonElement>(null)

  const { tasks, activeCount, cancellingIds, cancel, retry, clear, clearCompleted } =
    useTransferTasks(isOpen || isHovered)

  const updatePosition = useCallback(() => {
    if (!triggerRef.current) return
    const rect = triggerRef.current.getBoundingClientRect()
    const top = rect.bottom + 6
    const right = Math.max(12, window.innerWidth - rect.right)
    const maxWidth = Math.min(420, window.innerWidth - 24)
    setPopoverStyle({
      position: 'fixed',
      top: `${top}px`,
      right: `${right}px`,
      maxWidth: `${maxWidth}px`,
      zIndex: 10000
    })
  }, [])

  // Reposition on window resize or scroll
  useEffect(() => {
    if (!isOpen) return
    updatePosition()
    window.addEventListener('resize', updatePosition)
    window.addEventListener('scroll', updatePosition, true)
    return () => {
      window.removeEventListener('resize', updatePosition)
      window.removeEventListener('scroll', updatePosition, true)
    }
  }, [isOpen, updatePosition])

  // Close on Escape or click outside
  useEffect(() => {
    if (!isOpen) return

    const handleKeyDown = (e: KeyboardEvent): void => {
      if (e.key === 'Escape') {
        setIsOpen(false)
        triggerRef.current?.focus()
      }
    }

    const handleClickOutside = (e: MouseEvent): void => {
      const target = e.target as Node
      if (
        panelRef.current &&
        !panelRef.current.contains(target) &&
        triggerRef.current &&
        !triggerRef.current.contains(target)
      ) {
        setIsOpen(false)
      }
    }

    document.addEventListener('keydown', handleKeyDown)
    document.addEventListener('mousedown', handleClickOutside)
    return () => {
      document.removeEventListener('keydown', handleKeyDown)
      document.removeEventListener('mousedown', handleClickOutside)
    }
  }, [isOpen])

  const hasCompleted = tasks.some(
    (task) =>
      task.state === 'succeeded' || task.state === 'failed' || task.state === 'cancelled'
  )

  return (
    <div className="transfer-center-anchor">
      <button
        ref={triggerRef}
        type="button"
        className={`session-tab-btn session-transfer-toggle${isOpen ? ' is-active' : ''}`}
        aria-label={isOpen ? t('transfer.close') : t('transfer.open')}
        title={t('transfer.title')}
        aria-expanded={isOpen}
        onClick={() => setIsOpen((v) => !v)}
      >
        <FontAwesomeIcon icon={faRightLeft} className="session-transfer-icon" aria-hidden />
        <span className="session-transfer-label">{t('transfer.toggle')}</span>
        {activeCount > 0 && (
          <span className="session-transfer-badge" aria-label={t('transfer.activeCount', { count: activeCount })}>
            {activeCount > 99 ? '99+' : activeCount}
          </span>
        )}
      </button>

      {isOpen &&
        createPortal(
          <div
            ref={panelRef}
            className="transfer-center-popover glass"
            style={popoverStyle}
            role="region"
            aria-label={t('transfer.title')}
            onMouseEnter={() => setIsHovered(true)}
            onMouseLeave={() => setIsHovered(false)}
            onFocus={() => setIsHovered(true)}
            onBlur={(e) => {
              if (!panelRef.current?.contains(e.relatedTarget as Node)) {
                setIsHovered(false)
              }
            }}
          >
            <div className="transfer-center-header">
              <div className="transfer-center-title-row">
                <span className="transfer-center-title">{t('transfer.title')}</span>
                {activeCount > 0 && (
                  <span className="transfer-center-active-tag">
                    {t('transfer.activeCount', { count: activeCount })}
                  </span>
                )}
              </div>
              <div className="transfer-center-header-actions">
                {hasCompleted && (
                  <button
                    type="button"
                    className="transfer-btn-text"
                    onClick={() => void clearCompleted()}
                    title={t('transfer.clearCompleted')}
                  >
                    <FontAwesomeIcon icon={faTrashCan} aria-hidden />
                    <span>{t('transfer.clearCompleted')}</span>
                  </button>
                )}
                <button
                  type="button"
                  className="transfer-btn-close"
                  aria-label={t('transfer.close')}
                  onClick={() => setIsOpen(false)}
                >
                  <FontAwesomeIcon icon={faXmark} aria-hidden />
                </button>
              </div>
            </div>

            <div className="transfer-center-body">
              {tasks.length === 0 ? (
                <div className="transfer-center-empty">
                  <FontAwesomeIcon icon={faRightLeft} className="transfer-empty-icon" aria-hidden />
                  <p>{t('transfer.empty')}</p>
                </div>
              ) : (
                <ul className="transfer-task-list">
                  {tasks.map((task) => (
                    <TransferTaskCard
                      key={task.taskId}
                      task={task}
                      isCancelling={cancellingIds.has(task.taskId)}
                      onCancel={() => void cancel(task.taskId)}
                      onRetry={() => void retry(task.taskId)}
                      onClear={() => void clear(task.taskId)}
                    />
                  ))}
                </ul>
              )}
            </div>
          </div>,
          document.body
        )}
    </div>
  )
}

function TransferTaskCard({
  task,
  isCancelling,
  onCancel,
  onRetry,
  onClear
}: {
  task: TransferTaskWithMetrics
  isCancelling: boolean
  onCancel: () => void
  onRetry: () => void
  onClear: () => void
}): React.JSX.Element {
  const { t } = useTranslation()

  const isUpload = task.direction === 'upload'
  const isQueued = task.state === 'queued'
  const isRunning = task.state === 'running'
  const isFinalizing = task.state === 'finalizing'
  const isSucceeded = task.state === 'succeeded'
  const isFailed = task.state === 'failed'
  const isCancelled = task.state === 'cancelled'

  const percent =
    task.total > 0
      ? Math.min(100, Math.max(0, Math.round((task.transferred / task.total) * 100)))
      : isSucceeded
        ? 100
        : 0

  let etaDisplay = ''
  if (isRunning) {
    const rawEta = formatEta(task.eta, false)
    if (rawEta === 'calculating') {
      etaDisplay = t('transfer.calculating')
    } else if (rawEta === 'almostDone') {
      etaDisplay = t('transfer.almostDone')
    } else {
      etaDisplay = t('transfer.eta', { eta: rawEta })
    }
  } else if (isFinalizing) {
    etaDisplay = t('transfer.finalizing')
  }

  return (
    <li className={`transfer-task-card transfer-task-state-${task.state}`}>
      <div className="transfer-card-header">
        <div className="transfer-card-identity">
          <span
            className={`transfer-direction-icon transfer-dir-${task.direction}`}
            title={isUpload ? t('transfer.upload') : t('transfer.download')}
          >
            <FontAwesomeIcon icon={isUpload ? faArrowUp : faArrowDown} aria-hidden />
          </span>
          <span className="transfer-file-name" title={task.name}>
            {task.name}
          </span>
        </div>

        <div className="transfer-card-meta">
          {task.sessionTitle && (
            <span className="transfer-session-badge" title={task.sessionTitle}>
              {task.sessionTitle}
            </span>
          )}
          <span className={`transfer-state-badge transfer-badge-${task.state}`}>
            {isQueued && t('transfer.queued')}
            {isRunning && t('transfer.running')}
            {isFinalizing && t('transfer.finalizing')}
            {isSucceeded && (
              <>
                <FontAwesomeIcon icon={faCheck} aria-hidden className="transfer-state-icon" />
                {t('transfer.succeeded')}
              </>
            )}
            {isFailed && t('transfer.failed')}
            {isCancelled && (
              <>
                <FontAwesomeIcon icon={faBan} aria-hidden className="transfer-state-icon" />
                {t('transfer.cancelled')}
              </>
            )}
          </span>
        </div>
      </div>

      <div className="transfer-progress-wrapper">
        <div
          className={`transfer-progress-bar-bg${isRunning || isFinalizing ? ' is-animating' : ''}`}
          role="progressbar"
          aria-valuenow={percent}
          aria-valuemin={0}
          aria-valuemax={100}
        >
          <div
            className={`transfer-progress-bar-fill transfer-fill-${task.state}`}
            style={{ width: `${percent}%` }}
          />
        </div>
      </div>

      <div className="transfer-card-details">
        <div className="transfer-metrics">
          <span className="transfer-bytes">
            {formatBytes(task.transferred)}
            {task.total > 0 && ` / ${formatBytes(task.total)}`}
            {percent > 0 && ` (${percent}%)`}
          </span>
          {isRunning && task.speed > 0 && (
            <span className="transfer-speed">{formatSpeed(task.speed)}</span>
          )}
          {etaDisplay && <span className="transfer-eta">{etaDisplay}</span>}
        </div>

        <div className="transfer-actions">
          {(isQueued || isRunning) && (
            <button
              type="button"
              className="transfer-action-btn btn-cancel"
              onClick={onCancel}
              disabled={isCancelling}
              title={t('transfer.cancel')}
            >
              {isCancelling ? (
                <>
                  <FontAwesomeIcon icon={faSpinner} spin aria-hidden />
                  <span>{t('transfer.cancelling')}</span>
                </>
              ) : (
                <span>{t('transfer.cancel')}</span>
              )}
            </button>
          )}

          {(isFailed || isCancelled) && (
            <button
              type="button"
              className="transfer-action-btn btn-retry"
              onClick={onRetry}
              title={t('transfer.retry')}
            >
              <FontAwesomeIcon icon={faRotateRight} aria-hidden />
              <span>{t('transfer.retry')}</span>
            </button>
          )}

          {(isSucceeded || isFailed || isCancelled) && (
            <button
              type="button"
              className="transfer-action-btn btn-clear"
              onClick={onClear}
              title={t('transfer.clear')}
              aria-label={t('transfer.clear')}
            >
              <FontAwesomeIcon icon={faXmark} aria-hidden />
            </button>
          )}
        </div>
      </div>

      {isFailed && task.error && (
        <div className="transfer-error-box" title={task.error}>
          {task.error}
        </div>
      )}
    </li>
  )
}

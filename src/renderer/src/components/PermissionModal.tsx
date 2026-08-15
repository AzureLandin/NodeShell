import { useEffect, useRef } from 'react'
import { useTranslation } from 'react-i18next'
import type { PermissionAskEvent, PermissionDecision } from '../../../shared/types'

interface PermissionModalProps {
  request: PermissionAskEvent
  onDecide: (decision: PermissionDecision) => void
}

function toolLabelKey(tool: string): string {
  switch (tool) {
    case 'bash':
    case 'run_command':
      return 'permission.toolRunCommand'
    case 'sftp_write':
      return 'permission.toolSftpWrite'
    case 'sftp_upload':
      return 'permission.toolSftpUpload'
    case 'sftp_download':
      return 'permission.toolSftpDownload'
    default:
      return 'permission.toolUnknown'
  }
}

/** Instant permission prompt — no enter/exit motion (blocks a live tool call). */
export function PermissionModal({ request, onDecide }: PermissionModalProps): React.JSX.Element {
  const { t } = useTranslation()
  const overlayPointerDownRef = useRef(false)

  useEffect(() => {
    const onKey = (e: KeyboardEvent): void => {
      if (e.key === 'Escape') onDecide('deny')
    }
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  }, [onDecide])

  const source =
    request.source === 'mcp' ? t('permission.sourceMcp') : t('permission.sourceAgent')

  return (
    <div
      className="modal-overlay permission-modal-overlay"
      role="presentation"
      onPointerDown={(e) => {
        overlayPointerDownRef.current = e.target === e.currentTarget
      }}
      onClick={(e) => {
        if (!overlayPointerDownRef.current) return
        if (e.target !== e.currentTarget) return
        onDecide('deny')
      }}
    >
      <div
        className="modal confirm-modal permission-modal"
        role="dialog"
        aria-modal="true"
        aria-labelledby="permission-modal-title"
        onClick={(e) => e.stopPropagation()}
      >
        <h3 id="permission-modal-title" className="modal-title">
          {t('permission.title')}
        </h3>
        <p className="permission-modal-meta">
          <span className="permission-modal-source">{source}</span>
          {request.title ? (
            <span className="permission-modal-host">
              {t('permission.host', { title: request.title })}
            </span>
          ) : null}
        </p>
        <p className="permission-modal-action">{t(toolLabelKey(request.tool))}</p>
        {request.summary ? (
          <pre className="permission-modal-summary">{request.summary}</pre>
        ) : null}
        {request.detail ? <p className="permission-modal-detail">{request.detail}</p> : null}
        <div className="form-actions permission-modal-actions">
          <button type="button" className="btn-secondary" autoFocus onClick={() => onDecide('deny')}>
            {t('permission.deny')}
          </button>
          <button
            type="button"
            className="btn-secondary"
            onClick={() => onDecide('allow-session')}
          >
            {t('permission.allowSession')}
          </button>
          <button
            type="button"
            className="btn-primary"
            onClick={() => onDecide('allow')}
          >
            {t('permission.allow')}
          </button>
        </div>
      </div>
    </div>
  )
}

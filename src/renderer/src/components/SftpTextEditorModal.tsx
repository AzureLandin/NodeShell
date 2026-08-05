import { useCallback, useEffect, useRef, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { ConfirmModal } from './ConfirmModal'
import { ModalShell, useModalClose } from './ModalShell'
import { SftpCodeEditor } from './SftpCodeEditor'

export interface SftpTextEditorTarget {
  /** Basename used in the title and for size checks. */
  name: string
  /** Path passed to readText/writeText (relative name under cwd is fine). */
  remotePath: string
}

interface SftpTextEditorModalProps {
  sessionId: string
  target: SftpTextEditorTarget
  onClose: () => void
}

function SftpTextEditorBody({
  sessionId,
  target
}: {
  sessionId: string
  target: SftpTextEditorTarget
}): React.JSX.Element {
  const { t } = useTranslation()
  const requestClose = useModalClose()
  const [resolvedPath, setResolvedPath] = useState(target.remotePath)
  const [content, setContent] = useState('')
  const [baseline, setBaseline] = useState('')
  const [loading, setLoading] = useState(true)
  const [saving, setSaving] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const [confirmDiscard, setConfirmDiscard] = useState(false)
  /** Bumps when a fresh load finishes so CodeMirror remounts with that doc. */
  const [editorEpoch, setEditorEpoch] = useState(0)
  const dirty = content !== baseline
  const dirtyRef = useRef(dirty)
  dirtyRef.current = dirty
  const loadGenRef = useRef(0)

  useEffect(() => {
    const gen = ++loadGenRef.current
    setLoading(true)
    setError(null)
    setConfirmDiscard(false)
    void (async () => {
      try {
        const result = await window.api.sftp.readText(sessionId, target.remotePath)
        if (gen !== loadGenRef.current) return
        setResolvedPath(result.path)
        setContent(result.content)
        setBaseline(result.content)
        setEditorEpoch((n) => n + 1)
        setLoading(false)
      } catch (e) {
        if (gen !== loadGenRef.current) return
        setError(e instanceof Error ? e.message : t('sftp.error'))
        setLoading(false)
      }
    })()
  }, [sessionId, target.remotePath, t])

  const attemptClose = useCallback((): void => {
    if (dirtyRef.current) {
      setConfirmDiscard(true)
      return
    }
    requestClose()
  }, [requestClose])

  const handleSave = useCallback(async (): Promise<void> => {
    if (loading || saving) return
    setSaving(true)
    setError(null)
    try {
      const result = await window.api.sftp.writeText(sessionId, target.remotePath, content)
      setResolvedPath(result.path)
      setBaseline(content)
    } catch (e) {
      setError(e instanceof Error ? e.message : t('sftp.error'))
    } finally {
      setSaving(false)
    }
  }, [loading, saving, sessionId, target.remotePath, content, t])

  useEffect(() => {
    const onKey = (e: KeyboardEvent): void => {
      if ((e.ctrlKey || e.metaKey) && (e.key === 's' || e.key === 'S')) {
        e.preventDefault()
        void handleSave()
        return
      }
      if (e.key === 'Escape' && !confirmDiscard) {
        e.preventDefault()
        attemptClose()
      }
    }
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  }, [handleSave, attemptClose, confirmDiscard])

  const title = dirty ? `${target.name} *` : target.name

  return (
    <>
      <div className="settings-modal-header sftp-editor-header">
        <div className="sftp-editor-title-block">
          <h3 id="sftp-editor-title" className="modal-title">
            {t('sftp.editTitle', { name: title })}
          </h3>
          <p className="sftp-editor-path" title={resolvedPath}>
            {resolvedPath}
          </p>
        </div>
        <button
          type="button"
          className="settings-modal-close"
          aria-label={t('common.dismiss')}
          onClick={attemptClose}
        >
          ×
        </button>
      </div>

      {error && <p className="sftp-error sftp-editor-error">{error}</p>}

      <div className="sftp-editor-body">
        {loading ? (
          <p className="sftp-empty">{t('sftp.editLoading')}</p>
        ) : (
          <SftpCodeEditor
            key={`${resolvedPath}:${editorEpoch}`}
            initialValue={content}
            filename={target.name}
            readOnly={saving}
            aria-label={t('sftp.editTitle', { name: target.name })}
            onChange={setContent}
          />
        )}
      </div>

      <div className="form-actions sftp-editor-actions">
        <button type="button" className="btn-secondary" onClick={attemptClose} disabled={saving}>
          {t('common.cancel')}
        </button>
        <button
          type="button"
          className="btn-primary"
          onClick={() => void handleSave()}
          disabled={loading || saving || !dirty}
        >
          {saving ? t('sftp.editSaving') : t('sftp.editSave')}
        </button>
      </div>

      {confirmDiscard && (
        <ConfirmModal
          title={t('sftp.editDiscardTitle')}
          message={t('sftp.editDiscardMessage')}
          confirmLabel={t('sftp.editDiscardConfirm')}
          onConfirm={requestClose}
          onCancel={() => setConfirmDiscard(false)}
        />
      )}
    </>
  )
}

export function SftpTextEditorModal({
  sessionId,
  target,
  onClose
}: SftpTextEditorModalProps): React.JSX.Element {
  return (
    <ModalShell
      onClose={onClose}
      dialogClassName="sftp-editor-modal"
      labelledBy="sftp-editor-title"
      closeOnEscape={false}
      closeOnOverlayClick={false}
    >
      <SftpTextEditorBody sessionId={sessionId} target={target} />
    </ModalShell>
  )
}

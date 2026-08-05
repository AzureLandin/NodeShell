import { useCallback, useEffect, useRef, useState } from 'react'
import { useTranslation } from 'react-i18next'
import type { SftpTransferProgressEvent } from '../../../shared/types'
import { ConfirmModal } from './ConfirmModal'

interface SftpEntry {
  name: string
  path: string
  isDirectory: boolean
  size: number
  modifyTime: number
}

interface SftpPanelProps {
  sessionId: string | null
  connected: boolean
  expanded: boolean
  onToggle: () => void
}

function formatSize(bytes: number): string {
  if (bytes < 1024) return `${bytes} B`
  if (bytes < 1024 * 1024) return `${(bytes / 1024).toFixed(1)} KB`
  if (bytes < 1024 * 1024 * 1024) return `${(bytes / (1024 * 1024)).toFixed(1)} MB`
  return `${(bytes / (1024 * 1024 * 1024)).toFixed(1)} GB`
}

function formatTime(ms: number): string {
  if (!ms) return '—'
  try {
    return new Date(ms).toLocaleString(undefined, {
      year: 'numeric',
      month: '2-digit',
      day: '2-digit',
      hour: '2-digit',
      minute: '2-digit'
    })
  } catch {
    return '—'
  }
}

function transferPercent(event: SftpTransferProgressEvent): number | null {
  if (event.total <= 0) return null
  return Math.min(100, Math.round((event.transferred / event.total) * 100))
}

export function SftpPanel({
  sessionId,
  connected,
  expanded,
  onToggle
}: SftpPanelProps): React.JSX.Element {
  const { t } = useTranslation()
  const [cwd, setCwd] = useState('/')
  const [entries, setEntries] = useState<SftpEntry[]>([])
  const [loading, setLoading] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const [selectedPath, setSelectedPath] = useState<string | null>(null)
  const [transfer, setTransfer] = useState<SftpTransferProgressEvent | null>(null)
  const [dragOver, setDragOver] = useState(false)
  const [deleteTarget, setDeleteTarget] = useState<SftpEntry | null>(null)
  /** Session id whose listing is currently cached in UI state. */
  const loadedForSessionRef = useRef<string | null>(null)
  const requestGenRef = useRef(0)
  const transferClearRef = useRef<ReturnType<typeof setTimeout> | null>(null)
  const dragDepthRef = useRef(0)

  const refresh = useCallback(async (): Promise<void> => {
    if (!sessionId || !connected) {
      setEntries([])
      setCwd('/')
      setSelectedPath(null)
      loadedForSessionRef.current = null
      return
    }
    const gen = ++requestGenRef.current
    const forSession = sessionId
    setLoading(true)
    setError(null)
    try {
      const [path, list] = await Promise.all([
        window.api.sftp.cwd(forSession),
        window.api.sftp.list(forSession)
      ])
      if (gen !== requestGenRef.current) return
      setCwd(path)
      setEntries(list)
      loadedForSessionRef.current = forSession
    } catch (e) {
      if (gen !== requestGenRef.current) return
      setError(e instanceof Error ? e.message : t('sftp.error'))
    } finally {
      if (gen === requestGenRef.current) setLoading(false)
    }
  }, [sessionId, connected, t])

  // Load once per session when panel is shown; keep cache across collapse/expand.
  useEffect(() => {
    if (!connected || !sessionId) {
      if (loadedForSessionRef.current !== null) {
        setEntries([])
        setCwd('/')
        setError(null)
        setSelectedPath(null)
        setTransfer(null)
        loadedForSessionRef.current = null
      }
      return
    }
    if (expanded && loadedForSessionRef.current !== sessionId) {
      setTransfer(null)
      void refresh()
    }
  }, [expanded, connected, sessionId, refresh])

  useEffect(() => {
    const clearTimer = (): void => {
      if (transferClearRef.current) {
        clearTimeout(transferClearRef.current)
        transferClearRef.current = null
      }
    }
    const unsub = window.api.sftp.onTransferProgress((event) => {
      if (sessionId && event.sessionId !== sessionId) return
      clearTimer()
      setTransfer(event)
      if (event.done) {
        transferClearRef.current = setTimeout(() => {
          setTransfer((current) =>
            current &&
            current.name === event.name &&
            current.direction === event.direction &&
            current.done
              ? null
              : current
          )
          transferClearRef.current = null
        }, 1200)
      }
    })
    return () => {
      clearTimer()
      unsub()
    }
  }, [sessionId])

  // Native file drops (Wails OnFileDrop) arrive as events with absolute
  // paths — the DOM File objects carry none. The upload targets the session
  // that is current when the drop lands.
  useEffect(() => {
    if (!window.api.files.onDrop) return
    return window.api.files.onDrop((paths) => {
      if (!sessionId || !connected || paths.length === 0) return
      setError(null)
      void (async () => {
        try {
          await window.api.sftp.uploadPaths(sessionId, paths)
          await refresh()
        } catch (e) {
          setError(e instanceof Error ? e.message : t('sftp.error'))
        }
      })()
    })
  }, [sessionId, connected, refresh, t])

  const openDir = async (name: string): Promise<void> => {
    if (!sessionId) return
    const gen = ++requestGenRef.current
    const forSession = sessionId
    setLoading(true)
    setError(null)
    setSelectedPath(null)
    try {
      const path = await window.api.sftp.chdir(forSession, name)
      const list = await window.api.sftp.list(forSession)
      if (gen !== requestGenRef.current) return
      setCwd(path)
      setEntries(list)
      loadedForSessionRef.current = forSession
    } catch (e) {
      if (gen !== requestGenRef.current) return
      setError(e instanceof Error ? e.message : t('sftp.error'))
    } finally {
      if (gen === requestGenRef.current) setLoading(false)
    }
  }

  const handleMkdir = async (): Promise<void> => {
    if (!sessionId) return
    const name = window.prompt(t('sftp.mkdirPrompt'))
    if (!name?.trim()) return
    try {
      await window.api.sftp.mkdir(sessionId, name.trim())
      await refresh()
    } catch (e) {
      setError(e instanceof Error ? e.message : t('sftp.error'))
    }
  }

  const handleRename = async (entry: SftpEntry): Promise<void> => {
    if (!sessionId) return
    const name = window.prompt(t('sftp.renamePrompt'), entry.name)
    if (!name?.trim() || name === entry.name) return
    try {
      await window.api.sftp.rename(sessionId, entry.name, name.trim())
      await refresh()
    } catch (e) {
      setError(e instanceof Error ? e.message : t('sftp.error'))
    }
  }

  const handleDelete = (entry: SftpEntry): void => {
    if (!sessionId) return
    setDeleteTarget(entry)
  }

  const confirmDelete = async (): Promise<void> => {
    if (!sessionId || !deleteTarget) return
    const entry = deleteTarget
    setDeleteTarget(null)
    try {
      await window.api.sftp.remove(sessionId, entry.name)
      if (selectedPath === entry.path) setSelectedPath(null)
      await refresh()
    } catch (e) {
      setError(e instanceof Error ? e.message : t('sftp.error'))
    }
  }

  const handleUpload = async (): Promise<void> => {
    if (!sessionId) return
    try {
      await window.api.sftp.upload(sessionId)
      await refresh()
    } catch (e) {
      setError(e instanceof Error ? e.message : t('sftp.error'))
    }
  }

  const handleDownload = async (entry: SftpEntry): Promise<void> => {
    if (!sessionId || entry.isDirectory) return
    try {
      await window.api.sftp.download(sessionId, entry.name, entry.name)
    } catch (e) {
      setError(e instanceof Error ? e.message : t('sftp.error'))
    }
  }

  const resetDrag = (): void => {
    dragDepthRef.current = 0
    setDragOver(false)
  }

  const handleDragEnter = (e: React.DragEvent): void => {
    if (!sessionId || !connected) return
    e.preventDefault()
    e.stopPropagation()
    dragDepthRef.current += 1
    if (e.dataTransfer.types.includes('Files')) setDragOver(true)
  }

  const handleDragLeave = (e: React.DragEvent): void => {
    e.preventDefault()
    e.stopPropagation()
    dragDepthRef.current = Math.max(0, dragDepthRef.current - 1)
    if (dragDepthRef.current === 0) setDragOver(false)
  }

  const handleDragOver = (e: React.DragEvent): void => {
    if (!sessionId || !connected) return
    e.preventDefault()
    e.stopPropagation()
    e.dataTransfer.dropEffect = 'copy'
  }

  const handleDrop = async (e: React.DragEvent): Promise<void> => {
    e.preventDefault()
    e.stopPropagation()
    resetDrag()
    if (!sessionId || !connected) return
    // With native drops (Wails) the paths arrive via files.onDrop; the DOM
    // drop carries pathless File objects, so only the Electron path resolves
    // them here.
    if (window.api.files.onDrop) return

    const files = Array.from(e.dataTransfer.files)
    const paths: string[] = []
    for (const file of files) {
      try {
        const path = window.api.files.getPathForFile(file)
        if (path) paths.push(path)
      } catch {
        /* skip unreadable entries (e.g. some folder drops) */
      }
    }
    if (paths.length === 0) {
      setError(t('sftp.dropFilesOnly'))
      return
    }

    setError(null)
    try {
      await window.api.sftp.uploadPaths(sessionId, paths)
      await refresh()
    } catch (err) {
      setError(err instanceof Error ? err.message : t('sftp.error'))
    }
  }

  return (
    <div className={`sftp-panel${expanded ? ' sftp-panel-expanded' : ''}`}>
      <button type="button" className="sftp-panel-toggle" onClick={onToggle}>
        <span className="sftp-panel-chevron" aria-hidden>
          ▾
        </span>
        <span className="sftp-panel-title">{t('sftp.title')}</span>
        {connected && (
          <span className="sftp-status-dot" title={t('sftp.connected')} aria-hidden />
        )}
        {connected && (
          <span className="sftp-cwd" title={cwd}>
            {cwd}
          </span>
        )}
        {transfer && !transfer.done && (
          <span className="sftp-transfer-badge" title={transfer.name}>
            {transfer.direction === 'down' ? t('sftp.downloading') : t('sftp.uploading')}
            {transferPercent(transfer) !== null ? ` ${transferPercent(transfer)}%` : ''}
          </span>
        )}
      </button>

      <div
        className="sftp-panel-collapse"
        aria-hidden={!expanded}
        inert={!expanded ? true : undefined}
      >
        <div className="sftp-panel-collapse-inner">
          <div
            className={`sftp-panel-body${dragOver ? ' sftp-panel-body-dragover' : ''}`}
            onDragEnter={handleDragEnter}
            onDragLeave={handleDragLeave}
            onDragOver={handleDragOver}
            onDrop={(e) => void handleDrop(e)}
          >
          {dragOver && connected && sessionId && (
            <div className="sftp-drop-overlay" aria-hidden>
              <p>{t('sftp.dropToUpload')}</p>
            </div>
          )}
          {!connected || !sessionId ? (
            <div className="sftp-placeholder">
              <div className="sftp-placeholder-icon" aria-hidden />
              <p className="sftp-empty">{t('sftp.needSession')}</p>
            </div>
          ) : (
            <>
              <div className="sftp-toolbar">
                <div className="sftp-toolbar-group">
                  <button
                    type="button"
                    className="btn-secondary btn-sm"
                    onClick={() => void openDir('..')}
                    title={t('sftp.up')}
                  >
                    {t('sftp.up')}
                  </button>
                  <button
                    type="button"
                    className="btn-secondary btn-sm"
                    onClick={() => void refresh()}
                    disabled={loading}
                  >
                    {t('sftp.refresh')}
                  </button>
                </div>
                <div className="sftp-toolbar-group">
                  <button
                    type="button"
                    className="btn-primary btn-sm"
                    onClick={() => void handleUpload()}
                  >
                    {t('sftp.upload')}
                  </button>
                  <button
                    type="button"
                    className="btn-secondary btn-sm"
                    onClick={() => void handleMkdir()}
                  >
                    {t('sftp.mkdir')}
                  </button>
                </div>
              </div>

              {transfer && (
                <div
                  className={`sftp-transfer${transfer.done ? ' sftp-transfer-done' : ''}`}
                  role="status"
                  aria-live="polite"
                >
                  <div className="sftp-transfer-meta">
                    <span className="sftp-transfer-label">
                      {transfer.done
                        ? transfer.direction === 'down'
                          ? t('sftp.downloadDone')
                          : t('sftp.uploadDone')
                        : transfer.direction === 'down'
                          ? t('sftp.downloading')
                          : t('sftp.uploading')}
                    </span>
                    <span className="sftp-transfer-name" title={transfer.name}>
                      {transfer.name}
                    </span>
                    <span className="sftp-transfer-pct">
                      {transferPercent(transfer) !== null
                        ? `${transferPercent(transfer)}%`
                        : formatSize(transfer.transferred)}
                    </span>
                  </div>
                  <div className="sftp-transfer-track">
                    <div
                      className="sftp-transfer-fill"
                      style={{
                        width:
                          transferPercent(transfer) !== null
                            ? `${transferPercent(transfer)}%`
                            : transfer.done
                              ? '100%'
                              : '35%'
                      }}
                    />
                  </div>
                </div>
              )}

              {error && <p className="sftp-error">{error}</p>}

              <div className="sftp-browser">
                <div className="sftp-list-header" aria-hidden>
                  <span className="sftp-col-name">{t('sftp.colName')}</span>
                  <span className="sftp-col-size">{t('sftp.colSize')}</span>
                  <span className="sftp-col-mtime">{t('sftp.colModified')}</span>
                  <span className="sftp-col-actions" />
                </div>

                {loading && entries.length === 0 ? (
                  <div className="sftp-placeholder sftp-placeholder-inline">
                    <p className="sftp-empty">{t('sftp.loading')}</p>
                  </div>
                ) : entries.length === 0 ? (
                  <div className="sftp-placeholder sftp-placeholder-inline">
                    <p className="sftp-empty">{t('sftp.emptyDir')}</p>
                  </div>
                ) : (
                  <ul className={`sftp-list${loading ? ' sftp-list-loading' : ''}`}>
                    {entries.map((entry) => {
                      const selected = selectedPath === entry.path
                      return (
                        <li
                          key={entry.path}
                          className={`sftp-item${entry.isDirectory ? ' sftp-item-dir' : ''}${selected ? ' sftp-item-selected' : ''}`}
                        >
                          <button
                            type="button"
                            className="sftp-item-main"
                            onClick={() => setSelectedPath(entry.path)}
                            onDoubleClick={() => {
                              if (entry.isDirectory) void openDir(entry.name)
                              else void handleDownload(entry)
                            }}
                          >
                            <span className="sftp-col-name" title={entry.name}>
                              <span
                                className={`sftp-item-icon${entry.isDirectory ? ' sftp-item-icon-dir' : ' sftp-item-icon-file'}`}
                                aria-hidden
                              />
                              {entry.name}
                            </span>
                            <span className="sftp-col-size">
                              {entry.isDirectory ? '—' : formatSize(entry.size)}
                            </span>
                            <span className="sftp-col-mtime">{formatTime(entry.modifyTime)}</span>
                          </button>
                          <div className="sftp-item-actions">
                            {!entry.isDirectory && (
                              <button
                                type="button"
                                className="btn-secondary btn-sm"
                                onClick={() => void handleDownload(entry)}
                              >
                                {t('sftp.download')}
                              </button>
                            )}
                            <button
                              type="button"
                              className="btn-secondary btn-sm"
                              onClick={() => void handleRename(entry)}
                            >
                              {t('sftp.rename')}
                            </button>
                            <button
                              type="button"
                              className="btn-danger btn-sm"
                              onClick={() => handleDelete(entry)}
                            >
                              {t('sftp.delete')}
                            </button>
                          </div>
                        </li>
                      )
                    })}
                  </ul>
                )}
              </div>
            </>
          )}
          </div>
        </div>
      </div>

      {deleteTarget && (
        <ConfirmModal
          title={t('sftp.delete')}
          message={t('sftp.deleteConfirm', { name: deleteTarget.name })}
          confirmLabel={t('sftp.delete')}
          onConfirm={() => void confirmDelete()}
          onCancel={() => setDeleteTarget(null)}
        />
      )}
    </div>
  )
}

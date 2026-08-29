import { useCallback, useEffect, useRef, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { FontAwesomeIcon } from '@fortawesome/react-fontawesome'
import { faFile, faFolder } from '@fortawesome/free-solid-svg-icons'
import { isEditableTextFile, MAX_EDITABLE_TEXT_BYTES } from '../../../shared/editable-text'
import { ConfirmModal } from './ConfirmModal'
import { SftpContextMenu } from './SftpContextMenu'
import { SftpTextEditorModal, type SftpTextEditorTarget } from './SftpTextEditorModal'

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

export function SftpPanel({
  sessionId,
  connected,
  expanded,
  onToggle
}: SftpPanelProps): React.JSX.Element {
  const { t } = useTranslation()
  const [cwd, setCwd] = useState('/')
  const [pathDraft, setPathDraft] = useState('/')
  const [entries, setEntries] = useState<SftpEntry[]>([])
  const [loading, setLoading] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const [selectedPath, setSelectedPath] = useState<string | null>(null)
  const [dragOver, setDragOver] = useState(false)
  const [deleteTarget, setDeleteTarget] = useState<SftpEntry | null>(null)
  const [editTarget, setEditTarget] = useState<SftpTextEditorTarget | null>(null)
  const [menu, setMenu] = useState<{ x: number; y: number; entry: SftpEntry } | null>(null)
  /** Session id whose listing is currently cached in UI state. */
  const loadedForSessionRef = useRef<string | null>(null)
  const requestGenRef = useRef(0)
  const dragDepthRef = useRef(0)

  const refresh = useCallback(async (): Promise<void> => {
    if (!sessionId || !connected) {
      setEntries([])
      setCwd('/')
      setPathDraft('/')
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
      setPathDraft(path)
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
        setPathDraft('/')
        setError(null)
        setSelectedPath(null)
        setEditTarget(null)
        setMenu(null)
        loadedForSessionRef.current = null
      }
      return
    }
    if (expanded && loadedForSessionRef.current !== sessionId) {
      void refresh()
    }
  }, [expanded, connected, sessionId, refresh])

  useEffect(() => {
    if (!expanded) setMenu(null)
  }, [expanded])

  useEffect(() => {
    if (!menu) return
    const onMouseDown = (e: MouseEvent): void => {
      // The opening right-click is mousedown → contextmenu. Ignore button 2 so
      // that listener cannot dismiss the menu in the same gesture.
      if (e.button === 2) return
      const target = e.target as HTMLElement
      if (target.closest('[data-testid="sftp-context-menu"]')) return
      setMenu(null)
    }
    const onKeyDown = (e: KeyboardEvent): void => {
      if (e.key !== 'Escape') return
      e.preventDefault()
      setMenu(null)
    }
    window.addEventListener('mousedown', onMouseDown)
    window.addEventListener('keydown', onKeyDown)
    return () => {
      window.removeEventListener('mousedown', onMouseDown)
      window.removeEventListener('keydown', onKeyDown)
    }
  }, [menu])

  const cwdRef = useRef(cwd)
  cwdRef.current = cwd

  // Listen for completed transfer tasks for the current session to refresh file listing
  useEffect(() => {
    if (!window.api.transfer?.onTask) return
    return window.api.transfer.onTask((task) => {
      if (
        task.state === 'succeeded' &&
        task.sessionId === sessionId &&
        task.direction === 'upload' &&
        (!task.remotePath || task.remotePath === cwdRef.current)
      ) {
        void refresh()
      }
    })
  }, [sessionId, refresh])

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
          await window.api.transfer.enqueueUpload(sessionId, cwdRef.current, paths)
        } catch (e) {
          setError(e instanceof Error ? e.message : t('sftp.error'))
        }
      })()
    })
  }, [sessionId, connected, t])

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
      setPathDraft(path)
      setEntries(list)
      loadedForSessionRef.current = forSession
    } catch (e) {
      if (gen !== requestGenRef.current) return
      setError(e instanceof Error ? e.message : t('sftp.error'))
      setPathDraft(cwd)
    } finally {
      if (gen === requestGenRef.current) setLoading(false)
    }
  }

  const commitPath = async (): Promise<void> => {
    const next = pathDraft.trim()
    if (!next || next === cwd) {
      setPathDraft(cwd)
      return
    }
    await openDir(next)
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
      await window.api.transfer.chooseUploadFiles(sessionId, cwd)
    } catch (e) {
      setError(e instanceof Error ? e.message : t('sftp.error'))
    }
  }

  const handleDownload = async (entry: SftpEntry): Promise<void> => {
    if (!sessionId || entry.isDirectory) return
    try {
      await window.api.transfer.chooseDownloadTarget(sessionId, entry.path, entry.name)
    } catch (e) {
      setError(e instanceof Error ? e.message : t('sftp.error'))
    }
  }

  const openEditor = (entry: SftpEntry): void => {
    if (!sessionId || entry.isDirectory) return
    if (entry.size > MAX_EDITABLE_TEXT_BYTES) {
      setError(t('sftp.editTooLarge'))
      return
    }
    setError(null)
    setEditTarget({ name: entry.name, remotePath: entry.name })
  }

  const openEntryMenu = (entry: SftpEntry, e: React.MouseEvent): void => {
    e.preventDefault()
    e.stopPropagation()
    setSelectedPath(entry.path)
    setMenu({ x: e.clientX, y: e.clientY, entry })
  }

  const handleFileActivate = (entry: SftpEntry): void => {
    if (entry.isDirectory) {
      void openDir(entry.name)
      return
    }
    if (isEditableTextFile(entry.name)) {
      openEditor(entry)
      return
    }
    void handleDownload(entry)
  }

  const resetDrag = (): void => {
    dragDepthRef.current = 0
    setDragOver(false)
  }

  const handleDragEnter = (e: React.DragEvent): void => {
    if (!sessionId || !connected) return
    e.preventDefault()
    dragDepthRef.current += 1
    if (e.dataTransfer.types.includes('Files')) setDragOver(true)
  }

  const handleDragLeave = (e: React.DragEvent): void => {
    e.preventDefault()
    dragDepthRef.current = Math.max(0, dragDepthRef.current - 1)
    if (dragDepthRef.current === 0) setDragOver(false)
  }

  const handleDragOver = (e: React.DragEvent): void => {
    if (!sessionId || !connected) return
    e.preventDefault()
    e.dataTransfer.dropEffect = 'copy'
  }

  const handleDrop = async (e: React.DragEvent): Promise<void> => {
    e.preventDefault()
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
      await window.api.transfer.enqueueUpload(sessionId, cwd, paths)
    } catch (err) {
      setError(err instanceof Error ? err.message : t('sftp.error'))
    }
  }

  return (
    <div className={`sftp-panel glass${expanded ? ' sftp-panel-expanded' : ''}`}>
      <button type="button" className="sftp-panel-toggle" onClick={onToggle}>
        <span className="sftp-panel-chevron" aria-hidden>
          {expanded ? '▾' : '▴'}
        </span>
        <span className="sftp-panel-title">{t('sftp.title')}</span>
        {connected && (
          <span className="sftp-status-dot" title={t('sftp.connected')} aria-hidden />
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
              <FontAwesomeIcon icon={faFolder} className="sftp-placeholder-icon" aria-hidden />
              <p className="sftp-empty">{t('sftp.needSession')}</p>
            </div>
          ) : (
            <>
              <div className="sftp-path-bar">
                <input
                  type="text"
                  className="sftp-path-input"
                  value={pathDraft}
                  spellCheck={false}
                  autoComplete="off"
                  aria-label={t('sftp.path')}
                  onChange={(e) => setPathDraft(e.target.value)}
                  onBlur={() => void commitPath()}
                  onKeyDown={(e) => {
                    if (e.key === 'Enter') {
                      e.preventDefault()
                      void commitPath()
                    } else if (e.key === 'Escape') {
                      e.preventDefault()
                      setPathDraft(cwd)
                    }
                  }}
                />
              </div>
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

              {error && <p className="sftp-error">{error}</p>}

              <div className="sftp-browser">
                <div className="sftp-list-header" aria-hidden>
                  <span className="sftp-col-name">{t('sftp.colName')}</span>
                  <span className="sftp-col-size">{t('sftp.colSize')}</span>
                  <span className="sftp-col-mtime">{t('sftp.colModified')}</span>
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
                          onContextMenu={(e) => openEntryMenu(entry, e)}
                        >
                          <button
                            type="button"
                            className="sftp-item-main"
                            onClick={() => setSelectedPath(entry.path)}
                            onDoubleClick={() => handleFileActivate(entry)}
                            onContextMenu={(e) => openEntryMenu(entry, e)}
                          >
                            <span className="sftp-col-name" title={entry.name}>
                              <FontAwesomeIcon
                                icon={entry.isDirectory ? faFolder : faFile}
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

      {menu && (
        <SftpContextMenu
          x={menu.x}
          y={menu.y}
          canEdit={!menu.entry.isDirectory}
          canDownload={!menu.entry.isDirectory}
          onEdit={() => openEditor(menu.entry)}
          onDownload={() => void handleDownload(menu.entry)}
          onRename={() => void handleRename(menu.entry)}
          onDelete={() => handleDelete(menu.entry)}
          onClose={() => setMenu(null)}
        />
      )}

      {deleteTarget && (
        <ConfirmModal
          title={t('sftp.delete')}
          message={t('sftp.deleteConfirm', { name: deleteTarget.name })}
          confirmLabel={t('sftp.delete')}
          onConfirm={() => void confirmDelete()}
          onCancel={() => setDeleteTarget(null)}
        />
      )}

      {editTarget && sessionId && (
        <SftpTextEditorModal
          sessionId={sessionId}
          target={editTarget}
          onClose={() => setEditTarget(null)}
        />
      )}
    </div>
  )
}

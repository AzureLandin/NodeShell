import styles from './sftp.module.css'
import { bindStyles } from '../../ui/class-names'
import { useCallback, useEffect, useLayoutEffect, useRef, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { FontAwesomeIcon } from '@fortawesome/react-fontawesome'
import { faFile, faFolder } from '@fortawesome/free-solid-svg-icons'
import { isEditableTextFile, MAX_EDITABLE_TEXT_BYTES } from '../../../../shared/editable-text'
import { ConfirmModal } from '../../ui/ConfirmModal'
import { SftpContextMenu } from './SftpContextMenu'
import { SftpTextEditorModal, type SftpTextEditorTarget } from './SftpTextEditorModal'

const classes = bindStyles(styles)

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

function atDirectory(directory: string, path: string): string {
  return path.startsWith('/') ? path : `${directory.replace(/\/$/, '')}/${path}`
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
  const [editTarget, setEditTarget] = useState<
    (SftpTextEditorTarget & { sessionId: string }) | null
  >(null)
  const [menu, setMenu] = useState<{ x: number; y: number; entry: SftpEntry } | null>(null)
  const menuFocusRef = useRef<HTMLElement | null>(null)
  /** Session id whose listing is currently cached in UI state. */
  const loadedForSessionRef = useRef<string | null>(null)
  const requestGenRef = useRef(0)
  const dragDepthRef = useRef(0)
  const lifetime = useRef(0)
  const currentSession = useRef<string | null>(null)
  const directoryWork = useRef<Promise<void>>(Promise.resolve())

  const isCurrent = useCallback(
    (id: string, generation: number): boolean =>
      currentSession.current === id && lifetime.current === generation,
    []
  )

  useLayoutEffect(() => {
    lifetime.current += 1
    currentSession.current = connected ? sessionId : null
    directoryWork.current = Promise.resolve()
    loadedForSessionRef.current = null
    setEntries([])
    setCwd('/')
    setPathDraft('/')
    setSelectedPath(null)
    setLoading(false)
    setError(null)
    setMenu(null)
    setDeleteTarget(null)
    setEditTarget(null)
    setDragOver(false)
    dragDepthRef.current = 0
    return () => {
      lifetime.current += 1
      requestGenRef.current += 1
      currentSession.current = null
    }
  }, [sessionId, connected])

  // chdir mutates backend cwd. Serialize directory work within this session;
  // an IPC already sent cannot be cancelled here.
  const enqueueDirectory = useCallback((work: () => Promise<void>): Promise<void> => {
    const next = directoryWork.current.then(work, work)
    directoryWork.current = next
    return next
  }, [])

  const refresh = useCallback(async (): Promise<void> => {
    const generation = lifetime.current
    if (!sessionId || !connected || !isCurrent(sessionId, generation)) return
    const gen = ++requestGenRef.current
    const forSession = sessionId
    setLoading(true)
    setError(null)
    return enqueueDirectory(async () => {
      if (!isCurrent(forSession, generation) || gen !== requestGenRef.current) return
      try {
        const [path, list] = await Promise.all([
          window.api.sftp.cwd(forSession),
          window.api.sftp.list(forSession)
        ])
        if (!isCurrent(forSession, generation) || gen !== requestGenRef.current) return
        setCwd(path)
        setPathDraft(path)
        setEntries(list)
        loadedForSessionRef.current = forSession
      } catch (e) {
        if (!isCurrent(forSession, generation) || gen !== requestGenRef.current) return
        setError(e instanceof Error ? e.message : t('sftp.error'))
      } finally {
        if (isCurrent(forSession, generation) && gen === requestGenRef.current) setLoading(false)
      }
    })
  }, [sessionId, connected, t, enqueueDirectory, isCurrent])

  // Load once per session when panel is shown; keep cache across collapse/expand.
  useEffect(() => {
    if (!connected || !sessionId) return
    if (expanded && loadedForSessionRef.current !== sessionId) {
      void refresh()
    }
  }, [expanded, connected, sessionId, refresh])

  useEffect(() => {
    // A menu target belongs to the listing's session and visibility lifetime.
    setMenu(null)
  }, [expanded, connected, sessionId])

  const cwdRef = useRef(cwd)
  cwdRef.current = cwd

  // Listen for completed transfer tasks for the current session to refresh file listing
  useEffect(() => {
    if (!window.api.transfer?.onTask) return
    return window.api.transfer.onTask((task) => {
      if (
        connected &&
        task.state === 'succeeded' &&
        task.sessionId === sessionId &&
        task.direction === 'upload' &&
        (!task.remotePath || task.remotePath === cwdRef.current)
      ) {
        void refresh()
      }
    })
  }, [sessionId, connected, refresh])

  // Native file drops (Wails OnFileDrop) arrive as events with absolute
  // paths — the DOM File objects carry none. The upload targets the session
  // that is current when the drop lands.
  useEffect(() => {
    if (!window.api.files.onDrop) return
    return window.api.files.onDrop((paths) => {
      if (!sessionId || !connected || paths.length === 0) return
      const generation = lifetime.current
      setError(null)
      void (async () => {
        try {
          await window.api.transfer.enqueueUpload(sessionId, cwdRef.current, paths)
        } catch (e) {
          if (isCurrent(sessionId, generation))
            setError(e instanceof Error ? e.message : t('sftp.error'))
        }
      })()
    })
  }, [sessionId, connected, t, isCurrent])

  const openDir = async (name: string): Promise<void> => {
    const generation = lifetime.current
    if (!sessionId || !isCurrent(sessionId, generation)) return
    const gen = ++requestGenRef.current
    const forSession = sessionId
    const target = atDirectory(cwd, name)
    setLoading(true)
    setError(null)
    setSelectedPath(null)
    return enqueueDirectory(async () => {
      // A later refresh must observe this requested cwd, rather than discard
      // a navigation that has not reached IPC yet.
      if (!isCurrent(forSession, generation)) return
      try {
        const path = await window.api.sftp.chdir(forSession, target)
        if (!isCurrent(forSession, generation) || gen !== requestGenRef.current) return
        const list = await window.api.sftp.list(forSession)
        if (!isCurrent(forSession, generation) || gen !== requestGenRef.current) return
        setCwd(path)
        setPathDraft(path)
        setEntries(list)
        loadedForSessionRef.current = forSession
      } catch (e) {
        if (!isCurrent(forSession, generation) || gen !== requestGenRef.current) return
        setError(e instanceof Error ? e.message : t('sftp.error'))
        setPathDraft(cwd)
        // An earlier queued navigation may already have changed backend cwd.
        // Reconcile it after a failed later navigation instead of showing the
        // old listing under a directory that backend commands no longer use.
        try {
          const [path, list] = await Promise.all([
            window.api.sftp.cwd(forSession),
            window.api.sftp.list(forSession)
          ])
          if (!isCurrent(forSession, generation) || gen !== requestGenRef.current) return
          setCwd(path)
          setPathDraft(path)
          setEntries(list)
          loadedForSessionRef.current = forSession
        } catch {
          // Keep the original navigation error and the last displayed snapshot.
        }
      } finally {
        if (isCurrent(forSession, generation) && gen === requestGenRef.current) setLoading(false)
      }
    })
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
    const generation = lifetime.current
    const name = window.prompt(t('sftp.mkdirPrompt'))
    if (!name?.trim()) return
    try {
      await window.api.sftp.mkdir(sessionId, atDirectory(cwd, name.trim()))
      if (!isCurrent(sessionId, generation)) return
      await refresh()
    } catch (e) {
      if (isCurrent(sessionId, generation))
        setError(e instanceof Error ? e.message : t('sftp.error'))
    }
  }

  const handleRename = async (entry: SftpEntry): Promise<void> => {
    if (!sessionId) return
    const generation = lifetime.current
    const name = window.prompt(t('sftp.renamePrompt'), entry.name)
    if (!name?.trim() || name === entry.name) return
    try {
      await window.api.sftp.rename(sessionId, entry.path, atDirectory(cwd, name.trim()))
      if (!isCurrent(sessionId, generation)) return
      await refresh()
    } catch (e) {
      if (isCurrent(sessionId, generation))
        setError(e instanceof Error ? e.message : t('sftp.error'))
    }
  }

  const handleDelete = (entry: SftpEntry): void => {
    if (!sessionId) return
    setDeleteTarget(entry)
  }

  const confirmDelete = async (): Promise<void> => {
    if (!sessionId || !deleteTarget) return
    const generation = lifetime.current
    const entry = deleteTarget
    setDeleteTarget(null)
    try {
      await window.api.sftp.remove(sessionId, entry.path)
      if (!isCurrent(sessionId, generation)) return
      if (selectedPath === entry.path) setSelectedPath(null)
      await refresh()
    } catch (e) {
      if (isCurrent(sessionId, generation))
        setError(e instanceof Error ? e.message : t('sftp.error'))
    }
  }

  const handleUpload = async (): Promise<void> => {
    if (!sessionId) return
    const generation = lifetime.current
    try {
      await window.api.transfer.chooseUploadFiles(sessionId, cwd)
    } catch (e) {
      if (isCurrent(sessionId, generation))
        setError(e instanceof Error ? e.message : t('sftp.error'))
    }
  }

  const handleDownload = async (entry: SftpEntry): Promise<void> => {
    if (!sessionId || entry.isDirectory) return
    const generation = lifetime.current
    try {
      await window.api.transfer.chooseDownloadTarget(sessionId, entry.path, entry.name)
    } catch (e) {
      if (isCurrent(sessionId, generation))
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
    setEditTarget({ name: entry.name, remotePath: entry.path, sessionId })
  }

  const openEntryMenu = (entry: SftpEntry, e: React.MouseEvent): void => {
    e.preventDefault()
    e.stopPropagation()
    setSelectedPath(entry.path)
    menuFocusRef.current =
      e.currentTarget instanceof HTMLButtonElement
        ? e.currentTarget
        : e.currentTarget.querySelector('button')
    setMenu({ x: e.clientX, y: e.clientY, entry })
  }

  const handleFileActivate = (entry: SftpEntry): void => {
    if (entry.isDirectory) {
      void openDir(entry.path)
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
    const generation = lifetime.current
    try {
      await window.api.transfer.enqueueUpload(sessionId, cwd, paths)
    } catch (err) {
      if (isCurrent(sessionId, generation))
        setError(err instanceof Error ? err.message : t('sftp.error'))
    }
  }

  return (
    <div className={classes(`sftp-panel glass${expanded ? ' sftp-panel-expanded' : ''}`)}>
      <button type="button" className={classes('sftp-panel-toggle')} onClick={onToggle}>
        <span className={classes('sftp-panel-chevron')} aria-hidden>
          {expanded ? '▾' : '▴'}
        </span>
        <span className={classes('sftp-panel-title')}>{t('sftp.title')}</span>
        {connected && (
          <span className={classes('sftp-status-dot')} title={t('sftp.connected')} aria-hidden />
        )}
      </button>

      <div
        className={classes('sftp-panel-collapse')}
        aria-hidden={!expanded}
        inert={!expanded ? true : undefined}
      >
        <div className={classes('sftp-panel-collapse-inner')}>
          <div
            className={classes(`sftp-panel-body${dragOver ? ' sftp-panel-body-dragover' : ''}`)}
            onDragEnter={handleDragEnter}
            onDragLeave={handleDragLeave}
            onDragOver={handleDragOver}
            onDrop={(e) => void handleDrop(e)}
          >
            {dragOver && connected && sessionId && (
              <div className={classes('sftp-drop-overlay')} aria-hidden>
                <p>{t('sftp.dropToUpload')}</p>
              </div>
            )}
            {!connected || !sessionId ? (
              <div className={classes('sftp-placeholder')}>
                <FontAwesomeIcon
                  icon={faFolder}
                  className={classes('sftp-placeholder-icon')}
                  aria-hidden
                />
                <p className={classes('sftp-empty')}>{t('sftp.needSession')}</p>
              </div>
            ) : (
              <>
                <div className={classes('sftp-path-bar')}>
                  <input
                    type="text"
                    className={classes('sftp-path-input')}
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
                <div className={classes('sftp-toolbar')}>
                  <div className={classes('sftp-toolbar-group')}>
                    <button
                      type="button"
                      className={classes('btn-secondary btn-sm')}
                      onClick={() => void openDir('..')}
                      title={t('sftp.up')}
                    >
                      {t('sftp.up')}
                    </button>
                    <button
                      type="button"
                      className={classes('btn-secondary btn-sm')}
                      onClick={() => void refresh()}
                      disabled={loading}
                    >
                      {t('sftp.refresh')}
                    </button>
                  </div>
                  <div className={classes('sftp-toolbar-group')}>
                    <button
                      type="button"
                      className={classes('btn-primary btn-sm')}
                      onClick={() => void handleUpload()}
                    >
                      {t('sftp.upload')}
                    </button>
                    <button
                      type="button"
                      className={classes('btn-secondary btn-sm')}
                      onClick={() => void handleMkdir()}
                    >
                      {t('sftp.mkdir')}
                    </button>
                  </div>
                </div>

                {error && <p className={classes('sftp-error')}>{error}</p>}

                <div className={classes('sftp-browser')}>
                  <div className={classes('sftp-list-header')} aria-hidden>
                    <span className={classes('sftp-col-name')}>{t('sftp.colName')}</span>
                    <span className={classes('sftp-col-size')}>{t('sftp.colSize')}</span>
                    <span className={classes('sftp-col-mtime')}>{t('sftp.colModified')}</span>
                  </div>

                  {loading && entries.length === 0 ? (
                    <div className={classes('sftp-placeholder sftp-placeholder-inline')}>
                      <p className={classes('sftp-empty')}>{t('sftp.loading')}</p>
                    </div>
                  ) : entries.length === 0 ? (
                    <div className={classes('sftp-placeholder sftp-placeholder-inline')}>
                      <p className={classes('sftp-empty')}>{t('sftp.emptyDir')}</p>
                    </div>
                  ) : (
                    <ul className={classes(`sftp-list${loading ? ' sftp-list-loading' : ''}`)}>
                      {entries.map((entry) => {
                        const selected = selectedPath === entry.path
                        return (
                          <li
                            key={entry.path}
                            className={classes(
                              `sftp-item${entry.isDirectory ? ' sftp-item-dir' : ''}${selected ? ' sftp-item-selected' : ''}`
                            )}
                            onContextMenu={(e) => openEntryMenu(entry, e)}
                          >
                            <button
                              type="button"
                              className={classes('sftp-item-main')}
                              onClick={() => setSelectedPath(entry.path)}
                              onDoubleClick={() => handleFileActivate(entry)}
                              onContextMenu={(e) => openEntryMenu(entry, e)}
                            >
                              <span className={classes('sftp-col-name')} title={entry.name}>
                                <FontAwesomeIcon
                                  icon={entry.isDirectory ? faFolder : faFile}
                                  className={classes(
                                    `sftp-item-icon${entry.isDirectory ? ' sftp-item-icon-dir' : ' sftp-item-icon-file'}`
                                  )}
                                  aria-hidden
                                />
                                {entry.name}
                              </span>
                              <span className={classes('sftp-col-size')}>
                                {entry.isDirectory ? '—' : formatSize(entry.size)}
                              </span>
                              <span className={classes('sftp-col-mtime')}>
                                {formatTime(entry.modifyTime)}
                              </span>
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

      <SftpContextMenu
        open={menu !== null}
        x={menu?.x ?? 0}
        y={menu?.y ?? 0}
        canEdit={Boolean(menu && !menu.entry.isDirectory)}
        canDownload={Boolean(menu && !menu.entry.isDirectory)}
        onEdit={() => {
          if (menu) openEditor(menu.entry)
        }}
        onDownload={() => {
          if (menu) void handleDownload(menu.entry)
        }}
        onRename={() => {
          if (menu) void handleRename(menu.entry)
        }}
        onDelete={() => {
          if (menu) handleDelete(menu.entry)
        }}
        onClose={() => setMenu(null)}
        finalFocus={
          editTarget || deleteTarget
            ? false
            : () => (menuFocusRef.current?.isConnected ? menuFocusRef.current : false)
        }
      />

      {deleteTarget && (
        <ConfirmModal
          title={t('sftp.delete')}
          message={t('sftp.deleteConfirm', { name: deleteTarget.name })}
          confirmLabel={t('sftp.delete')}
          onConfirm={() => void confirmDelete()}
          onCancel={() => setDeleteTarget(null)}
        />
      )}

      {editTarget && sessionId && connected && editTarget.sessionId === sessionId && (
        <SftpTextEditorModal
          sessionId={sessionId}
          target={editTarget}
          onClose={() => setEditTarget(null)}
        />
      )}
    </div>
  )
}

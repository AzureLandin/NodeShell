import styles from './hosts.module.css'
import { bindStyles } from '../../ui/class-names'
import { useEffect, useMemo, useRef, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { FontAwesomeIcon } from '@fortawesome/react-fontawesome'
import {
  faBolt,
  faArrowsRotate,
  faMagnifyingGlass,
  faPen,
  faPlus,
  faServer,
  faTrash,
  faXmark
} from '@fortawesome/free-solid-svg-icons'
import type { HostConfig } from '../../../../shared/types'
import { parseIpcThrownError } from '../../../../shared/ipc-error'
import { Button } from '../../ui/button'
import { IconButton } from '../../ui/icon-button'
import { ConfirmModal } from '../../ui/ConfirmModal'
import { HostForm, type HostFormSubmit } from './HostForm'
import { ModalShell, useModalClose } from '../../ui/ModalShell'

const classes = bindStyles(styles)

interface HostPickerModalProps {
  hosts: HostConfig[]
  loading?: boolean
  refreshing?: boolean
  loadError?: string | null
  hasData?: boolean
  onRefresh?: () => void
  connecting?: boolean
  connectingHost?: HostConfig | null
  onConnect: (host: HostConfig) => void
  onCreate: (result: HostFormSubmit) => Promise<void>
  onUpdate: (id: string, result: HostFormSubmit) => Promise<void>
  onRemove: (id: string) => Promise<void>
  onClose: () => void
}

type FormMode = { type: 'create' } | { type: 'edit'; host: HostConfig }

function HostPickerModalBody({
  hosts,
  connecting,
  connectingHost,
  onConnect,
  onCreate,
  onUpdate,
  onRemove,
  formMode,
  setFormMode,
  submitting,
  setSubmitting,
  removing,
  setRemoving,
  loading,
  refreshing,
  loadError,
  hasData,
  onRefresh
}: {
  hosts: HostConfig[]
  connecting: boolean
  connectingHost: HostConfig | null
  onConnect: (host: HostConfig) => void
  onCreate: (result: HostFormSubmit) => Promise<void>
  onUpdate: (id: string, result: HostFormSubmit) => Promise<void>
  onRemove: (id: string) => Promise<void>
  formMode: FormMode | null
  setFormMode: React.Dispatch<React.SetStateAction<FormMode | null>>
  submitting: boolean
  setSubmitting: (submitting: boolean) => void
  removing: boolean
  setRemoving: (removing: boolean) => void
  loading: boolean
  refreshing: boolean
  loadError: string | null
  hasData: boolean
  onRefresh?: () => void
}): React.JSX.Element {
  const { t } = useTranslation()
  const requestClose = useModalClose()
  const [searchTerm, setSearchTerm] = useState('')
  const [pendingDelete, setPendingDelete] = useState<HostConfig | null>(null)
  const [commandError, setCommandError] = useState<string | null>(null)
  const removePending = useRef(false)
  const busy = connecting || submitting || removing

  const filteredHosts = useMemo(() => {
    const q = searchTerm.trim().toLowerCase()
    if (!q) return hosts
    return hosts.filter(
      (h) =>
        h.name.toLowerCase().includes(q) ||
        h.host.toLowerCase().includes(q) ||
        h.username.toLowerCase().includes(q)
    )
  }, [hosts, searchTerm])

  const handleDelete = (host: HostConfig): void => {
    setCommandError(null)
    setPendingDelete(host)
  }

  const confirmDelete = async (): Promise<void> => {
    if (!pendingDelete || removePending.current) return
    const host = pendingDelete
    removePending.current = true
    setRemoving(true)
    setCommandError(null)
    setPendingDelete(null)
    try {
      await onRemove(host.id)
      setFormMode((current) =>
        current?.type === 'edit' && current.host.id === host.id ? null : current
      )
    } catch (error) {
      setCommandError(parseIpcThrownError(error).message || t('hostsPicker.deleteFailed'))
    } finally {
      removePending.current = false
      setRemoving(false)
    }
  }

  const handleFormSubmit = async (result: HostFormSubmit): Promise<void> => {
    const submittedMode = formMode
    if (formMode?.type === 'edit') {
      await onUpdate(formMode.host.id, result)
    } else {
      await onCreate(result)
    }
    setFormMode((current) => (current === submittedMode ? null : current))
  }

  const hasHosts = hosts.length > 0

  return (
    <>
      <div className={classes('host-picker-header')}>
        <div className={classes('host-picker-title-block')}>
          <div className={classes('host-picker-title-row')}>
            <FontAwesomeIcon
              icon={faServer}
              className={classes('host-picker-header-icon')}
              aria-hidden="true"
            />
            <h3 id="host-picker-title" className={classes('modal-title')}>
              {t('hostsPicker.title')}
            </h3>
          </div>
          <p className={classes('modal-subtitle')}>{t('hostsPicker.subtitle')}</p>
        </div>
        <div className={classes('host-picker-header-actions')}>
          {onRefresh && (
            <IconButton
              aria-label={t('hostsPicker.refresh')}
              title={t('hostsPicker.refresh')}
              onClick={onRefresh}
              disabled={busy || refreshing}
            >
              <FontAwesomeIcon icon={faArrowsRotate} />
            </IconButton>
          )}
          <Button
            type="button"
            variant="primary"
            size="small"
            onClick={() => setFormMode({ type: 'create' })}
            disabled={busy || loading || (!hasData && Boolean(loadError))}
            startIcon={<FontAwesomeIcon icon={faPlus} />}
          >
            {t('hosts.new')}
          </Button>
          <IconButton
            type="button"
            className={classes('settings-modal-close')}
            aria-label={t('common.dismiss')}
            onClick={requestClose}
            disabled={busy}
          >
            <FontAwesomeIcon icon={faXmark} />
          </IconButton>
        </div>
      </div>

      {loadError && (
        <div className={classes('host-query-feedback')} role="alert">
          <p>{t(hasData ? 'hostsPicker.refreshFailed' : 'hostsPicker.loadFailed')}</p>
          <p>{loadError}</p>
          {onRefresh && (
            <Button
              variant="secondary"
              size="small"
              onClick={onRefresh}
              disabled={busy || refreshing}
            >
              {t('hostsPicker.retry')}
            </Button>
          )}
        </div>
      )}
      {commandError && (
        <p className={classes('host-query-feedback form-inline-error')} role="alert">
          {commandError}
        </p>
      )}
      {refreshing && (
        <p className={classes('host-query-feedback')} role="status">
          {t('hostsPicker.loading')}
        </p>
      )}

      {!hasData && (loading || loadError) ? null : !hasHosts ? (
        formMode ? (
          <div className={classes('host-form-overlay host-form-overlay-full')}>
            <HostForm
              initial={
                formMode.type === 'edit' ? { ...formMode.host, id: formMode.host.id } : undefined
              }
              onSubmit={handleFormSubmit}
              onSubmittingChange={setSubmitting}
              onCancel={() => setFormMode(null)}
            />
          </div>
        ) : (
          <div className={classes('host-empty')}>
            <div className={classes('host-empty-icon-wrap')} aria-hidden="true">
              <FontAwesomeIcon icon={faServer} className={classes('host-empty-icon')} />
            </div>
            <p className={classes('host-empty-title')}>{t('hostsPicker.empty')}</p>
            <p className={classes('host-empty-description')}>{t('hostsPicker.emptyHint')}</p>
          </div>
        )
      ) : (
        <div className={classes('host-picker-body')}>
          <aside className={classes('host-directory')}>
            <div className={classes('host-search-bar')}>
              <div className={classes('host-search-input-wrap')}>
                <FontAwesomeIcon
                  icon={faMagnifyingGlass}
                  className={classes('host-search-icon')}
                  aria-hidden="true"
                />
                <input
                  type="text"
                  className={classes('host-search-input')}
                  placeholder={t('hostsPicker.search')}
                  value={searchTerm}
                  onChange={(e) => setSearchTerm(e.target.value)}
                  aria-label={t('hostsPicker.search')}
                />
                {searchTerm && (
                  <button
                    type="button"
                    className={classes('host-search-clear')}
                    onClick={() => setSearchTerm('')}
                    aria-label={t('hostsPicker.clearSearch')}
                  >
                    <FontAwesomeIcon icon={faXmark} aria-hidden="true" />
                  </button>
                )}
              </div>
              <div className={classes('host-directory-count')}>
                {t('hostsPicker.hostCount', { count: filteredHosts.length })}
              </div>
            </div>

            <ul className={classes('host-directory-list')} role="list">
              {filteredHosts.length === 0 ? (
                <li className={classes('host-search-empty')}>
                  <p className={classes('host-search-empty-text')}>
                    {t('hostsPicker.noSearchResult')}
                  </p>
                  <button
                    type="button"
                    className={classes('btn-secondary btn-sm')}
                    onClick={() => setSearchTerm('')}
                  >
                    {t('hostsPicker.clearSearch')}
                  </button>
                </li>
              ) : (
                filteredHosts.map((host) => {
                  const initial = host.name.trim().charAt(0).toUpperCase() || '#'
                  const authLabel =
                    host.authMethod === 'privateKey' ? t('form.privateKey') : t('form.password')
                  const isTarget = connecting && connectingHost?.id === host.id
                  const isSelected = formMode?.type === 'edit' && formMode.host.id === host.id

                  return (
                    <li
                      key={host.id}
                      className={classes(`host-item${isSelected ? ' is-selected' : ''}`)}
                      aria-current={isSelected ? 'true' : undefined}
                      onClick={() => {
                        if (!busy) setFormMode({ type: 'edit', host })
                      }}
                    >
                      <div className={classes('host-item-top')}>
                        <div className={classes('host-item-avatar')} aria-hidden="true">
                          {initial}
                        </div>
                        <div className={classes('host-item-headline')}>
                          <span className={classes('host-item-name')} title={host.name}>
                            {host.name}
                          </span>
                          <span className={classes('host-item-badge')}>{authLabel}</span>
                        </div>
                      </div>

                      <div className={classes('host-item-endpoint')}>
                        <span
                          className={classes('host-item-endpoint-text')}
                          title={`${host.username}@${host.host}:${host.port}`}
                        >
                          {host.username}@{host.host}:{host.port}
                        </span>
                      </div>

                      <div
                        className={classes('host-item-actions')}
                        onClick={(e) => e.stopPropagation()}
                      >
                        <Button
                          type="button"
                          variant="primary"
                          size="small"
                          className={classes('host-item-btn-connect')}
                          disabled={busy}
                          onClick={() => onConnect(host)}
                          title={isTarget ? t('auth.connecting') : t('hosts.connect')}
                          startIcon={<FontAwesomeIcon icon={faBolt} />}
                        >
                          {isTarget ? t('auth.connecting') : t('hosts.connect')}
                        </Button>
                        <Button
                          type="button"
                          variant="secondary"
                          size="small"
                          disabled={busy}
                          onClick={() => {
                            if (!busy) setFormMode({ type: 'edit', host })
                          }}
                          title={t('hosts.edit')}
                          aria-label={t('hosts.edit')}
                          startIcon={<FontAwesomeIcon icon={faPen} />}
                        >
                          {t('hosts.edit')}
                        </Button>
                        <Button
                          type="button"
                          variant="danger"
                          size="small"
                          disabled={busy}
                          onClick={() => handleDelete(host)}
                          title={t('hosts.delete')}
                          aria-label={t('hosts.delete')}
                          startIcon={<FontAwesomeIcon icon={faTrash} />}
                        >
                          {t('hosts.delete')}
                        </Button>
                      </div>
                    </li>
                  )
                })
              )}
            </ul>
          </aside>

          <main className={classes('host-editor')}>
            {formMode ? (
              <HostForm
                initial={
                  formMode.type === 'edit' ? { ...formMode.host, id: formMode.host.id } : undefined
                }
                onSubmit={handleFormSubmit}
                onSubmittingChange={setSubmitting}
                onCancel={() => setFormMode(null)}
              />
            ) : (
              <div className={classes('host-editor-placeholder')}>
                <div className={classes('host-editor-placeholder-icon')} aria-hidden="true">
                  <FontAwesomeIcon icon={faServer} />
                </div>
                <h4 className={classes('host-editor-placeholder-title')}>
                  {t('hostsPicker.selectPromptTitle')}
                </h4>
                <p className={classes('host-editor-placeholder-hint')}>
                  {t('hostsPicker.selectPromptHint')}
                </p>
                <Button
                  type="button"
                  variant="primary"
                  size="small"
                  onClick={() => setFormMode({ type: 'create' })}
                  disabled={busy}
                  startIcon={<FontAwesomeIcon icon={faPlus} />}
                >
                  {t('hosts.new')}
                </Button>
              </div>
            )}
          </main>
        </div>
      )}

      {pendingDelete && (
        <ConfirmModal
          title={t('hosts.delete')}
          message={t('hosts.deleteConfirm', { name: pendingDelete.name })}
          confirmLabel={t('common.confirm')}
          cancelLabel={t('common.cancel')}
          onConfirm={() => void confirmDelete()}
          onCancel={() => setPendingDelete(null)}
        />
      )}
    </>
  )
}

export function HostPickerModal({
  hosts,
  loading = false,
  refreshing = false,
  loadError = null,
  hasData = true,
  onRefresh,
  connecting = false,
  connectingHost = null,
  onConnect,
  onCreate,
  onUpdate,
  onRemove,
  onClose
}: HostPickerModalProps): React.JSX.Element {
  const [formMode, setFormMode] = useState<FormMode | null>(null)
  const [submitting, setSubmitting] = useState(false)
  const [removing, setRemoving] = useState(false)

  useEffect(() => {
    const onKey = (e: KeyboardEvent): void => {
      if (e.key === 'Escape' && formMode && !connecting && !submitting && !removing)
        setFormMode(null)
    }
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  }, [formMode, connecting, submitting, removing])

  return (
    <ModalShell
      onClose={onClose}
      dialogClassName={classes('host-picker-modal')}
      labelledBy="host-picker-title"
      closeOnEscape={!formMode && !connecting && !submitting && !removing}
      closeOnOverlayClick={!connecting && !submitting && !removing}
    >
      <HostPickerModalBody
        hosts={hosts}
        connecting={connecting}
        connectingHost={connectingHost}
        onConnect={onConnect}
        onCreate={onCreate}
        onUpdate={onUpdate}
        onRemove={onRemove}
        formMode={formMode}
        setFormMode={setFormMode}
        submitting={submitting}
        setSubmitting={setSubmitting}
        removing={removing}
        setRemoving={setRemoving}
        loading={loading}
        refreshing={refreshing}
        loadError={loadError}
        hasData={hasData}
        onRefresh={onRefresh}
      />
    </ModalShell>
  )
}

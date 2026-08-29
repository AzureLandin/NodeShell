import { useEffect, useMemo, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { FontAwesomeIcon } from '@fortawesome/react-fontawesome'
import {
  faBolt,
  faMagnifyingGlass,
  faPen,
  faPlus,
  faServer,
  faTrash,
  faXmark
} from '@fortawesome/free-solid-svg-icons'
import type { HostConfig } from '../../../shared/types'
import { ConfirmModal } from './ConfirmModal'
import { HostForm, type HostFormSubmit } from './HostForm'
import { ModalShell, useModalClose } from './ModalShell'

interface HostPickerModalProps {
  hosts: HostConfig[]
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
  setFormMode
}: {
  hosts: HostConfig[]
  connecting: boolean
  connectingHost: HostConfig | null
  onConnect: (host: HostConfig) => void
  onCreate: (result: HostFormSubmit) => Promise<void>
  onUpdate: (id: string, result: HostFormSubmit) => Promise<void>
  onRemove: (id: string) => Promise<void>
  formMode: FormMode | null
  setFormMode: (mode: FormMode | null) => void
}): React.JSX.Element {
  const { t } = useTranslation()
  const requestClose = useModalClose()
  const [searchTerm, setSearchTerm] = useState('')
  const [pendingDelete, setPendingDelete] = useState<HostConfig | null>(null)

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
    setPendingDelete(host)
  }

  const confirmDelete = async (): Promise<void> => {
    if (!pendingDelete) return
    const host = pendingDelete
    setPendingDelete(null)
    if (formMode?.type === 'edit' && formMode.host.id === host.id) {
      setFormMode(null)
    }
    await onRemove(host.id)
  }

  const handleFormSubmit = async (result: HostFormSubmit): Promise<void> => {
    if (formMode?.type === 'edit') {
      await onUpdate(formMode.host.id, result)
    } else {
      await onCreate(result)
    }
    setFormMode(null)
  }

  const hasHosts = hosts.length > 0

  return (
    <>
      <div className="host-picker-header">
        <div className="host-picker-title-block">
          <div className="host-picker-title-row">
            <FontAwesomeIcon
              icon={faServer}
              className="host-picker-header-icon"
              aria-hidden="true"
            />
            <h3 id="host-picker-title" className="modal-title">
              {t('hostsPicker.title')}
            </h3>
          </div>
          <p className="modal-subtitle">{t('hostsPicker.subtitle')}</p>
        </div>
        <div className="host-picker-header-actions">
          <button
            type="button"
            className="btn-primary btn-sm"
            onClick={() => setFormMode({ type: 'create' })}
            disabled={connecting}
          >
            <FontAwesomeIcon icon={faPlus} aria-hidden="true" />
            <span>{t('hosts.new')}</span>
          </button>
          <button
            type="button"
            className="settings-modal-close"
            aria-label={t('common.dismiss')}
            onClick={requestClose}
            disabled={connecting}
          >
            <FontAwesomeIcon icon={faXmark} aria-hidden="true" />
          </button>
        </div>
      </div>

      {!hasHosts ? (
        formMode ? (
          <div className="host-form-overlay host-form-overlay-full">
            <HostForm
              initial={
                formMode.type === 'edit'
                  ? { ...formMode.host, id: formMode.host.id }
                  : undefined
              }
              onSubmit={handleFormSubmit}
              onCancel={() => setFormMode(null)}
            />
          </div>
        ) : (
          <div className="host-empty">
            <div className="host-empty-icon-wrap" aria-hidden="true">
              <FontAwesomeIcon icon={faServer} className="host-empty-icon" />
            </div>
            <p className="host-empty-title">{t('hostsPicker.empty')}</p>
            <p className="host-empty-description">{t('hostsPicker.emptyHint')}</p>
          </div>
        )
      ) : (
        <div className="host-picker-body">
          <aside className="host-directory">
            <div className="host-search-bar">
              <div className="host-search-input-wrap">
                <FontAwesomeIcon
                  icon={faMagnifyingGlass}
                  className="host-search-icon"
                  aria-hidden="true"
                />
                <input
                  type="text"
                  className="host-search-input"
                  placeholder={t('hostsPicker.search')}
                  value={searchTerm}
                  onChange={(e) => setSearchTerm(e.target.value)}
                  aria-label={t('hostsPicker.search')}
                />
                {searchTerm && (
                  <button
                    type="button"
                    className="host-search-clear"
                    onClick={() => setSearchTerm('')}
                    aria-label={t('hostsPicker.clearSearch')}
                  >
                    <FontAwesomeIcon icon={faXmark} aria-hidden="true" />
                  </button>
                )}
              </div>
              <div className="host-directory-count">
                {t('hostsPicker.hostCount', { count: filteredHosts.length })}
              </div>
            </div>

            <ul className="host-directory-list" role="list">
              {filteredHosts.length === 0 ? (
                <li className="host-search-empty">
                  <p className="host-search-empty-text">
                    {t('hostsPicker.noSearchResult')}
                  </p>
                  <button
                    type="button"
                    className="btn-secondary btn-sm"
                    onClick={() => setSearchTerm('')}
                  >
                    {t('hostsPicker.clearSearch')}
                  </button>
                </li>
              ) : (
                filteredHosts.map((host) => {
                  const initial = host.name.trim().charAt(0).toUpperCase() || '#'
                  const authLabel =
                    host.authMethod === 'privateKey'
                      ? t('form.privateKey')
                      : t('form.password')
                  const isTarget = connecting && connectingHost?.id === host.id
                  const isSelected =
                    formMode?.type === 'edit' && formMode.host.id === host.id

                  return (
                    <li
                      key={host.id}
                      className={`host-item${isSelected ? ' is-selected' : ''}`}
                      aria-current={isSelected ? 'true' : undefined}
                      onClick={() => setFormMode({ type: 'edit', host })}
                    >
                      <div className="host-item-top">
                        <div className="host-item-avatar" aria-hidden="true">
                          {initial}
                        </div>
                        <div className="host-item-headline">
                          <span className="host-item-name" title={host.name}>
                            {host.name}
                          </span>
                          <span className="host-item-badge">{authLabel}</span>
                        </div>
                      </div>

                      <div className="host-item-endpoint">
                        <span className="host-item-endpoint-text" title={`${host.username}@${host.host}:${host.port}`}>
                          {host.username}@{host.host}:{host.port}
                        </span>
                      </div>

                      <div
                        className="host-item-actions"
                        onClick={(e) => e.stopPropagation()}
                      >
                        <button
                          type="button"
                          className="btn-primary btn-sm host-item-btn-connect"
                          disabled={connecting}
                          onClick={() => onConnect(host)}
                          title={isTarget ? t('auth.connecting') : t('hosts.connect')}
                        >
                          <FontAwesomeIcon icon={faBolt} aria-hidden="true" />
                          <span>
                            {isTarget ? t('auth.connecting') : t('hosts.connect')}
                          </span>
                        </button>
                        <button
                          type="button"
                          className="btn-secondary btn-sm"
                          disabled={connecting}
                          onClick={() => setFormMode({ type: 'edit', host })}
                          title={t('hosts.edit')}
                          aria-label={t('hosts.edit')}
                        >
                          <FontAwesomeIcon icon={faPen} aria-hidden="true" />
                          <span>{t('hosts.edit')}</span>
                        </button>
                        <button
                          type="button"
                          className="btn-danger btn-sm"
                          disabled={connecting}
                          onClick={() => handleDelete(host)}
                          title={t('hosts.delete')}
                          aria-label={t('hosts.delete')}
                        >
                          <FontAwesomeIcon icon={faTrash} aria-hidden="true" />
                          <span>{t('hosts.delete')}</span>
                        </button>
                      </div>
                    </li>
                  )
                })
              )}
            </ul>
          </aside>

          <main className="host-editor">
            {formMode ? (
              <HostForm
                key={formMode.type === 'edit' ? formMode.host.id : 'create'}
                initial={
                  formMode.type === 'edit'
                    ? { ...formMode.host, id: formMode.host.id }
                    : undefined
                }
                onSubmit={handleFormSubmit}
                onCancel={() => setFormMode(null)}
              />
            ) : (
              <div className="host-editor-placeholder">
                <div className="host-editor-placeholder-icon" aria-hidden="true">
                  <FontAwesomeIcon icon={faServer} />
                </div>
                <h4 className="host-editor-placeholder-title">
                  {t('hostsPicker.selectPromptTitle')}
                </h4>
                <p className="host-editor-placeholder-hint">
                  {t('hostsPicker.selectPromptHint')}
                </p>
                <button
                  type="button"
                  className="btn-primary btn-sm"
                  onClick={() => setFormMode({ type: 'create' })}
                  disabled={connecting}
                >
                  <FontAwesomeIcon icon={faPlus} aria-hidden="true" />
                  <span>{t('hosts.new')}</span>
                </button>
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
  connecting = false,
  connectingHost = null,
  onConnect,
  onCreate,
  onUpdate,
  onRemove,
  onClose
}: HostPickerModalProps): React.JSX.Element {
  const [formMode, setFormMode] = useState<FormMode | null>(null)

  useEffect(() => {
    const onKey = (e: KeyboardEvent): void => {
      if (e.key === 'Escape' && formMode && !connecting) setFormMode(null)
    }
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  }, [formMode, connecting])

  return (
    <ModalShell
      onClose={onClose}
      dialogClassName="host-picker-modal"
      labelledBy="host-picker-title"
      closeOnEscape={!formMode && !connecting}
      closeOnOverlayClick={!connecting}
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
      />
    </ModalShell>
  )
}

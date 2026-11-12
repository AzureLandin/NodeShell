import styles from './sessions.module.css'
import { bindStyles } from '../../ui/class-names'
import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import type { HostConfig } from '../../../../shared/types'
import { ModalShell, useModalClose } from '../../ui/ModalShell'
import { PasswordField } from '../../ui/PasswordField'

const classes = bindStyles(styles)

function PasswordModalBody({
  host,
  busy,
  error,
  onSubmit,
  onCancelConnect
}: {
  host: HostConfig
  busy: boolean
  error: string | null
  onSubmit: (password: string) => void
  onCancelConnect?: () => void
}): React.JSX.Element {
  const { t } = useTranslation()
  const requestClose = useModalClose()
  const [password, setPassword] = useState('')

  const handleSubmit = (e: React.FormEvent): void => {
    e.preventDefault()
    if (busy) return
    onSubmit(password)
  }

  return (
    <form onSubmit={handleSubmit} noValidate>
      <h3 id="password-modal-title" className={classes('modal-title')}>
        {t('auth.passwordTitle', { name: host.name })}
      </h3>
      <p className={classes('modal-subtitle')}>
        {host.username}@{host.host}:{host.port}
      </p>
      {busy && (
        <p className={classes('session-connecting-status')} role="status">
          {t('auth.connectingStatus', {
            name: host.name,
            host: host.host,
            port: host.port
          })}
        </p>
      )}
      <PasswordField
        label={t('auth.passwordLabel')}
        value={password}
        onChange={setPassword}
        autoFocus
        required
        disabled={busy}
        aria-invalid={Boolean(error)}
        aria-describedby={error ? 'password-modal-error' : undefined}
        className={classes(error ? 'input-invalid' : undefined)}
      />
      {error && (
        <p id="password-modal-error" className={classes('form-inline-error')} role="alert">
          {error}
        </p>
      )}
      <div className={classes('form-actions')}>
        {busy && onCancelConnect ? (
          <button type="button" className={classes('btn-secondary')} onClick={onCancelConnect}>
            {t('auth.cancelConnect')}
          </button>
        ) : (
          <button
            type="button"
            className={classes('btn-secondary')}
            onClick={requestClose}
            disabled={busy}
          >
            {t('form.cancel')}
          </button>
        )}
        <button type="submit" className={classes('btn-primary')} disabled={busy}>
          {busy ? t('auth.connecting') : t('auth.connect')}
        </button>
      </div>
    </form>
  )
}

export function PasswordModal({
  host,
  busy,
  error,
  onSubmit,
  onCancel,
  onCancelConnect
}: {
  host: HostConfig
  busy: boolean
  error: string | null
  onSubmit: (password: string) => void
  onCancel: () => void
  onCancelConnect?: () => void
}): React.JSX.Element {
  return (
    <ModalShell
      onClose={onCancel}
      dialogClassName={classes('password-modal')}
      labelledBy="password-modal-title"
      closeOnEscape={!busy}
      closeOnOverlayClick={false}
    >
      <PasswordModalBody
        host={host}
        busy={busy}
        error={error}
        onSubmit={onSubmit}
        onCancelConnect={onCancelConnect}
      />
    </ModalShell>
  )
}

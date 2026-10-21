import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { FontAwesomeIcon } from '@fortawesome/react-fontawesome'
import { faFolderOpen } from '@fortawesome/free-solid-svg-icons'
import type { AuthMethod, HostInput } from '../../../shared/types'
import { FormField } from '../ui/field'
import { Input } from '../ui/input'
import { PasswordField } from './PasswordField'
import { Select } from './Select'

export interface HostFormSubmit {
  input: HostInput
  /** Ephemeral password for connect / optional credential update — never written to hosts.json */
  password?: string
}

interface HostFormProps {
  initial?: Partial<HostInput> & { id?: string }
  onSubmit: (result: HostFormSubmit) => Promise<void> | void
  onCancel: () => void
}

export function HostForm({ initial, onSubmit, onCancel }: HostFormProps): React.JSX.Element {
  const { t } = useTranslation()
  const isEdit = Boolean(initial?.id)
  const [name, setName] = useState(initial?.name ?? '')
  const [host, setHost] = useState(initial?.host ?? '')
  const [port, setPort] = useState(String(initial?.port ?? 22))
  const [username, setUsername] = useState(initial?.username ?? '')
  const [authMethod, setAuthMethod] = useState<AuthMethod>(initial?.authMethod ?? 'password')
  const [password, setPassword] = useState('')
  const [privateKeyPath, setPrivateKeyPath] = useState(initial?.privateKeyPath ?? '')
  const [formError, setFormError] = useState<string | null>(null)
  const [submitting, setSubmitting] = useState(false)

  const handleBrowse = async (): Promise<void> => {
    const path = await window.api.dialog.openPrivateKeyFile()
    if (path) setPrivateKeyPath(path)
  }

  const handleSubmit = async (e: React.FormEvent): Promise<void> => {
    e.preventDefault()
    setFormError(null)
    const portNum = Number(port)
    if (!name.trim() || !host.trim() || !username.trim() || !Number.isFinite(portNum)) return
    if (portNum < 1 || portNum > 65535) {
      setFormError(t('form.portInvalid'))
      return
    }

    if (authMethod === 'password') {
      if (!isEdit && !password) {
        setFormError(t('form.passwordRequired'))
        return
      }
    } else if (!privateKeyPath.trim()) {
      if (!isEdit || !initial?.privateKeyPath) {
        setFormError(t('form.privateKeyRequired'))
        return
      }
    }

    const input: HostInput = {
      name: name.trim(),
      host: host.trim(),
      port: portNum,
      username: username.trim(),
      authMethod,
      ...(authMethod === 'privateKey' && privateKeyPath.trim()
        ? { privateKeyPath: privateKeyPath.trim() }
        : {})
    }

    setSubmitting(true)
    try {
      await onSubmit({
        input,
        ...(authMethod === 'password' && password ? { password } : {})
      })
    } catch (err) {
      setFormError(err instanceof Error ? err.message : t('form.saveFailed'))
    } finally {
      setSubmitting(false)
    }
  }

  return (
    <form className="host-form" onSubmit={(e) => void handleSubmit(e)} noValidate>
      <div className="host-form-header">
        <h3 className="host-form-title">{isEdit ? t('form.editTitle') : t('form.newTitle')}</h3>
      </div>

      <div className="host-form-body">
        <div className="form-section">
          <h4 className="form-section-title">{t('hostsPicker.basicInfo')}</h4>
          <FormField label={t('form.name')}>
            <Input
              type="text"
              value={name}
              onChange={(e) => setName(e.target.value)}
              required
              autoFocus
              disabled={submitting}
            />
          </FormField>

          <div className="form-grid-2col">
            <FormField label={t('form.host')} className="form-field form-field-host">
              <Input
                type="text"
                value={host}
                onChange={(e) => setHost(e.target.value)}
                required
                placeholder="example.com"
                disabled={submitting}
              />
            </FormField>

            <FormField label={t('form.port')} className="form-field form-field-port">
              <Input
                type="number"
                value={port}
                onChange={(e) => setPort(e.target.value)}
                required
                min={1}
                max={65535}
                disabled={submitting}
              />
            </FormField>
          </div>

          <FormField label={t('form.username')}>
            <Input
              type="text"
              value={username}
              onChange={(e) => setUsername(e.target.value)}
              required
              disabled={submitting}
            />
          </FormField>
        </div>

        <div className="form-section">
          <h4 className="form-section-title">{t('hostsPicker.authConfig')}</h4>
          <FormField label={t('form.auth')}>
            <Select
              value={authMethod}
              onChange={(v) => {
                setAuthMethod(v as AuthMethod)
                setFormError(null)
              }}
              aria-label={t('form.auth')}
              options={[
                { value: 'password', label: t('form.password') },
                { value: 'privateKey', label: t('form.privateKey') }
              ]}
            />
          </FormField>

          {authMethod === 'password' && (
            <PasswordField
              label={t('form.password')}
              value={password}
              onChange={(next) => {
                setPassword(next)
                setFormError(null)
              }}
              required={!isEdit}
              disabled={submitting}
              placeholder={isEdit ? t('form.passwordLeaveBlank') : undefined}
              aria-invalid={Boolean(formError)}
              autoComplete="new-password"
            />
          )}

          {authMethod === 'privateKey' && (
            <FormField label={t('form.privateKeyPath')}>
              <div className="form-field-row">
                <Input
                  type="text"
                  value={privateKeyPath}
                  onChange={(e) => {
                    setPrivateKeyPath(e.target.value)
                    setFormError(null)
                  }}
                  placeholder="/path/to/key"
                  required={!isEdit}
                  disabled={submitting}
                />
                <button
                  type="button"
                  className="btn-secondary"
                  onClick={() => void handleBrowse()}
                  disabled={submitting}
                  title={t('form.browse')}
                  aria-label={t('form.browse')}
                >
                  <FontAwesomeIcon icon={faFolderOpen} aria-hidden="true" />
                  <span>{t('form.browse')}</span>
                </button>
              </div>
            </FormField>
          )}
        </div>

        {formError && (
          <p className="form-inline-error" role="alert">
            {formError}
          </p>
        )}
      </div>

      <div className="host-form-actions-bar">
        <button type="button" className="btn-secondary" onClick={onCancel} disabled={submitting}>
          {t('form.cancel')}
        </button>
        <button type="submit" className="btn-primary" disabled={submitting}>
          {submitting ? t('form.saving') : isEdit ? t('form.save') : t('form.createAndConnect')}
        </button>
      </div>
    </form>
  )
}

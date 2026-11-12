import styles from './hosts.module.css'
import { bindStyles } from '../../ui/class-names'
import { useLayoutEffect, useRef } from 'react'
import { Controller, useForm, useWatch, type FieldErrors } from 'react-hook-form'
import { useTranslation } from 'react-i18next'
import { FontAwesomeIcon } from '@fortawesome/react-fontawesome'
import { faFolderOpen } from '@fortawesome/free-solid-svg-icons'
import { parseIpcThrownError } from '../../../../shared/ipc-error'
import { Button } from '../../ui/button'
import { FormField } from '../../ui/field'
import { Input } from '../../ui/input'
import { PasswordField } from '../../ui/PasswordField'
import { Select } from '../../ui/select'
import {
  hostFormDefaults,
  hostFormSubmission,
  validateHostDraft,
  type HostFormDraft,
  type HostFormInitial,
  type HostFormSubmit
} from './host-form-model'

const classes = bindStyles(styles)

export type { HostFormSubmit } from './host-form-model'

interface HostFormProps {
  initial?: HostFormInitial
  onSubmit: (result: HostFormSubmit) => Promise<void> | void
  onCancel: () => void
  onSubmittingChange?: (submitting: boolean) => void
}

/** An editing identity owns its draft. Ordinary parent renders must not reset user input. */
export function HostForm(props: HostFormProps): React.JSX.Element {
  return (
    <HostFormFields key={props.initial?.id ? `edit:${props.initial.id}` : 'create'} {...props} />
  )
}

function HostFormFields({
  initial,
  onSubmit,
  onCancel,
  onSubmittingChange
}: HostFormProps): React.JSX.Element {
  const { t } = useTranslation()
  const isEdit = Boolean(initial?.id)
  const {
    register,
    control,
    handleSubmit,
    resetField,
    setValue,
    setError,
    clearErrors,
    getValues,
    formState: { errors, isSubmitting }
  } = useForm<HostFormDraft>({
    defaultValues: hostFormDefaults(initial),
    resolver: (values) => {
      const validation = validateHostDraft(values, initial)
      const errors: FieldErrors<HostFormDraft> = {}
      for (const [name, message] of Object.entries(validation)) {
        errors[name as keyof HostFormDraft] = { type: 'validate', message }
      }
      return Object.keys(errors).length ? { values: {}, errors } : { values, errors: {} }
    }
  })
  const authMethod = useWatch({ control, name: 'authMethod' })
  const mounted = useRef(true)
  const submitPending = useRef(false)
  const browseGeneration = useRef(0)
  const busyCallback = useRef(onSubmittingChange)
  busyCallback.current = onSubmittingChange

  useLayoutEffect(() => {
    busyCallback.current?.(isSubmitting)
  }, [isSubmitting])

  useLayoutEffect(() => {
    mounted.current = true
    return () => {
      mounted.current = false
      browseGeneration.current += 1
      resetField('password')
      busyCallback.current?.(false)
    }
  }, [resetField])

  const fieldError = (name: keyof HostFormDraft): string | undefined => {
    const message = errors[name]?.message
    return message ? t(message) : undefined
  }

  const handleBrowse = async (): Promise<void> => {
    const generation = ++browseGeneration.current
    try {
      const path = await window.api.dialog.openPrivateKeyFile()
      if (!mounted.current || generation !== browseGeneration.current || submitPending.current)
        return
      if (getValues('authMethod') !== 'privateKey') return
      if (path) {
        setValue('privateKeyPath', path, { shouldDirty: true, shouldValidate: true })
        clearErrors('root')
      }
    } catch (err) {
      if (mounted.current && generation === browseGeneration.current) {
        setError('root.server', {
          message: parseIpcThrownError(err).message || t('form.saveFailed')
        })
      }
    }
  }

  const save = async (draft: HostFormDraft): Promise<void> => {
    // Validation can finish after the editor has been replaced or closed.
    if (!mounted.current) return
    clearErrors('root')
    try {
      await onSubmit(hostFormSubmission(draft))
      if (mounted.current) resetField('password')
    } catch (err) {
      if (mounted.current) {
        setError('root.server', {
          message: parseIpcThrownError(err).message || t('form.saveFailed')
        })
      }
    }
  }

  return (
    <form
      className={classes('host-form')}
      noValidate
      aria-busy={isSubmitting}
      onChange={() => clearErrors('root')}
      onSubmit={(event) => {
        event.preventDefault()
        // Block a second event synchronously, before async validation publishes isSubmitting.
        if (submitPending.current) return
        submitPending.current = true
        browseGeneration.current += 1
        void handleSubmit(save)(event).finally(() => {
          submitPending.current = false
        })
      }}
    >
      <div className={classes('host-form-header')}>
        <h3 className={classes('host-form-title')}>
          {isEdit ? t('form.editTitle') : t('form.newTitle')}
        </h3>
      </div>

      <div className={classes('host-form-body')}>
        <div className={classes('form-section')}>
          <h4 className={classes('form-section-title')}>{t('hostsPicker.basicInfo')}</h4>
          <FormField label={t('form.name')} error={fieldError('name')}>
            <Input type="text" {...register('name')} required autoFocus disabled={isSubmitting} />
          </FormField>

          <div className={classes('form-grid-2col')}>
            <FormField
              label={t('form.host')}
              error={fieldError('host')}
              className={classes('form-field form-field-host')}
            >
              <Input
                type="text"
                {...register('host')}
                required
                placeholder="example.com"
                disabled={isSubmitting}
              />
            </FormField>

            <FormField
              label={t('form.port')}
              error={fieldError('port')}
              className={classes('form-field form-field-port')}
            >
              <Input
                type="number"
                {...register('port')}
                required
                min={1}
                max={65535}
                step={1}
                disabled={isSubmitting}
              />
            </FormField>
          </div>

          <FormField label={t('form.username')} error={fieldError('username')}>
            <Input type="text" {...register('username')} required disabled={isSubmitting} />
          </FormField>
        </div>

        <div className={classes('form-section')}>
          <h4 className={classes('form-section-title')}>{t('hostsPicker.authConfig')}</h4>
          <Controller
            name="authMethod"
            control={control}
            render={({ field }) => (
              <FormField label={t('form.auth')}>
                <Select
                  value={field.value}
                  onChange={(value) => {
                    browseGeneration.current += 1
                    resetField('password')
                    clearErrors(['password', 'privateKeyPath'])
                    clearErrors('root')
                    field.onChange(value)
                  }}
                  disabled={isSubmitting}
                  aria-label={t('form.auth')}
                  options={[
                    { value: 'password', label: t('form.password') },
                    { value: 'privateKey', label: t('form.privateKey') }
                  ]}
                />
              </FormField>
            )}
          />

          {authMethod === 'password' && (
            <Controller
              name="password"
              control={control}
              shouldUnregister
              render={({ field }) => (
                <PasswordField
                  label={t('form.password')}
                  name={field.name}
                  inputRef={field.ref}
                  value={field.value ?? ''}
                  onChange={field.onChange}
                  onBlur={field.onBlur}
                  error={fieldError('password')}
                  required={!isEdit}
                  disabled={isSubmitting}
                  placeholder={isEdit ? t('form.passwordLeaveBlank') : undefined}
                  autoComplete="new-password"
                />
              )}
            />
          )}

          {authMethod === 'privateKey' && (
            <FormField label={t('form.privateKeyPath')} error={fieldError('privateKeyPath')}>
              <div className={classes('form-field-row')}>
                <Input
                  type="text"
                  {...register('privateKeyPath')}
                  placeholder="/path/to/key"
                  required={!isEdit || !initial?.privateKeyPath}
                  disabled={isSubmitting}
                />
                <Button
                  type="button"
                  variant="secondary"
                  onClick={() => void handleBrowse()}
                  disabled={isSubmitting}
                  title={t('form.browse')}
                  aria-label={t('form.browse')}
                  startIcon={<FontAwesomeIcon icon={faFolderOpen} />}
                >
                  {t('form.browse')}
                </Button>
              </div>
            </FormField>
          )}
        </div>

        {errors.root?.server?.message && (
          <p className={classes('form-inline-error')} role="alert">
            {errors.root.server.message}
          </p>
        )}
      </div>

      <div className={classes('host-form-actions-bar')}>
        <Button type="button" variant="secondary" onClick={onCancel} disabled={isSubmitting}>
          {t('form.cancel')}
        </Button>
        <Button type="submit" variant="primary" loading={isSubmitting}>
          {isSubmitting ? t('form.saving') : isEdit ? t('form.save') : t('form.createAndConnect')}
        </Button>
      </div>
    </form>
  )
}

import { useState, type ReactNode, type Ref } from 'react'
import { useTranslation } from 'react-i18next'
import { FormField } from './field'
import { Input } from './input'

interface PasswordFieldProps {
  id?: string
  label: string
  value: string
  onChange: (value: string) => void
  disabled?: boolean
  required?: boolean
  autoFocus?: boolean
  autoComplete?: string
  placeholder?: string
  'aria-invalid'?: boolean
  'aria-describedby'?: string
  className?: string
  name?: string
  inputRef?: Ref<HTMLInputElement>
  onBlur?: () => void
  error?: ReactNode
  description?: ReactNode
}

export function PasswordField({
  id,
  label,
  value,
  onChange,
  disabled,
  required,
  autoFocus,
  autoComplete = 'current-password',
  placeholder,
  'aria-invalid': ariaInvalid,
  'aria-describedby': ariaDescribedBy,
  className,
  name,
  inputRef,
  onBlur,
  error,
  description
}: PasswordFieldProps): React.JSX.Element {
  const { t } = useTranslation()
  const [visible, setVisible] = useState(false)

  return (
    <FormField label={label} error={error} description={description}>
      <div className="form-field-row password-field-row">
        <Input
          id={id}
          name={name}
          ref={inputRef}
          type={visible ? 'text' : 'password'}
          value={value}
          onChange={(e) => onChange(e.target.value)}
          onBlur={onBlur}
          disabled={disabled}
          required={required}
          autoFocus={autoFocus}
          autoComplete={autoComplete}
          placeholder={placeholder}
          aria-invalid={ariaInvalid}
          aria-describedby={ariaDescribedBy}
          className={className}
        />
        <button
          type="button"
          className="btn-secondary password-visibility-toggle"
          onClick={() => setVisible((v) => !v)}
          disabled={disabled}
          aria-pressed={visible}
          aria-label={visible ? t('form.hidePassword') : t('form.showPassword')}
          title={visible ? t('form.hidePassword') : t('form.showPassword')}
        >
          {visible ? t('form.hidePassword') : t('form.showPassword')}
        </button>
      </div>
    </FormField>
  )
}

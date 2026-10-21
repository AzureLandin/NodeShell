import { Field } from '@base-ui/react/field'
import type { ReactNode } from 'react'
import './forms.css'

export interface FormFieldProps extends Omit<Field.Root.Props, 'children'> {
  /** Visible field label. */
  label: ReactNode
  /** The control(s) the label points at. */
  children: ReactNode
  /** Wrapper class. Defaults to the shared `form-field` class from App.css. */
  className?: string
}

/**
 * Label + control pair built on Base UI `Field`.
 *
 * The label is rendered as a `<span>` instead of a native `<label>` so the
 * existing `.form-field > span` rules in App.css keep applying unchanged.
 * Base UI still wires the label to the control through `aria-labelledby`, and
 * clicking the label focuses the control.
 */
export function FormField({
  label,
  children,
  className,
  ...rootProps
}: FormFieldProps): React.JSX.Element {
  return (
    <Field.Root className={className ?? 'form-field'} {...rootProps}>
      <Field.Label className="form-field-label" nativeLabel={false} render={<span />}>
        {label}
      </Field.Label>
      {children}
    </Field.Root>
  )
}

export interface FormFieldErrorProps extends Field.Error.Props {
  children: ReactNode
}

/**
 * Inline validation message for a `FormField`.
 *
 * `role="alert"` keeps the message announced the same way the previous
 * hand-written `<p role="alert">` blocks were.
 */
export function FormFieldError({ children, ...props }: FormFieldErrorProps): React.JSX.Element {
  return (
    <Field.Error className="form-inline-error" role="alert" {...props}>
      {children}
    </Field.Error>
  )
}

export interface FormFieldRootProps extends Omit<Field.Root.Props, 'children'> {
  /** The control(s) the field tracks. */
  children: ReactNode
}

/**
 * Bare Base UI `Field.Root` for layouts that bring their own label markup
 * (for example composite editors such as the model id tag input).
 */
export function FormFieldRoot({ children, ...props }: FormFieldRootProps): React.JSX.Element {
  return <Field.Root {...props}>{children}</Field.Root>
}

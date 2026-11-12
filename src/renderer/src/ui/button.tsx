import { Button as BaseButton } from '@base-ui/react/button'
import type { ReactNode } from 'react'
import './button.css'

export interface ButtonProps extends BaseButton.Props {
  variant?: 'primary' | 'secondary' | 'danger'
  size?: 'small'
  loading?: boolean
  startIcon?: ReactNode
  endIcon?: ReactNode
}

/** Shared button semantics; visual variants reuse the existing product styles. */
export function Button({
  variant,
  size,
  loading = false,
  startIcon,
  endIcon,
  disabled,
  type = 'button',
  className,
  children,
  ...props
}: ButtonProps): React.JSX.Element {
  const classes = [variant ? `btn-${variant}` : '', size === 'small' ? 'btn-sm' : '']
  return (
    <BaseButton
      {...props}
      type={type}
      disabled={disabled || loading}
      aria-busy={loading || undefined}
      data-ui-button=""
      className={(state) =>
        [...classes, typeof className === 'function' ? className(state) : className]
          .filter(Boolean)
          .join(' ')
      }
    >
      {startIcon && <span aria-hidden="true">{startIcon}</span>}
      {children}
      {endIcon && <span aria-hidden="true">{endIcon}</span>}
    </BaseButton>
  )
}

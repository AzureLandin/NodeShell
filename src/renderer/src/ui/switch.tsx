import { Switch } from '@base-ui/react/switch'
import type { ReactNode } from 'react'
import './forms.css'

export interface FormSwitchProps extends Omit<Switch.Root.Props, 'children'> {
  /** Optional text rendered next to the switch. */
  children?: ReactNode
}

/**
 * Toggle built on Base UI `Switch`.
 *
 * The root is the track and the thumb is rendered inside it, so the visual
 * result matches a classic on/off switch.
 */
export function FormSwitch({ children, className, ...props }: FormSwitchProps): React.JSX.Element {
  return (
    <Switch.Root className={className ? `app-switch ${className}` : 'app-switch'} {...props}>
      <Switch.Thumb className="app-switch-thumb" />
      {children}
    </Switch.Root>
  )
}

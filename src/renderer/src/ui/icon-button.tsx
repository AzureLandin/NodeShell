import { Button, type ButtonProps } from './button'

export interface IconButtonProps extends Omit<ButtonProps, 'startIcon' | 'endIcon'> {
  'aria-label': string
}

/** Icon actions must supply their accessible name at the call site. */
export function IconButton({ children, ...props }: IconButtonProps): React.JSX.Element {
  return (
    <Button {...props}>
      <span aria-hidden="true">{children}</span>
    </Button>
  )
}

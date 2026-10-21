import { Button as BaseButton } from '@base-ui/react/button'

export type ButtonProps = BaseButton.Props

/**
 * Button built on Base UI `Button`.
 *
 * Renders a native `<button type="button">`, so buttons inside forms keep
 * their previous behaviour (no implicit submit).
 */
export function Button(props: ButtonProps): React.JSX.Element {
  return <BaseButton {...props} />
}

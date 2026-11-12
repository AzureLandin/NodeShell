import { AlertDialog as BaseAlertDialog } from '@base-ui/react/alert-dialog'
import type { DialogClassName } from './dialog'
import './dialog.css'

/**
 * Thin wrappers around `@base-ui/react/alert-dialog`.
 *
 * An alert dialog asks the user to confirm an action: it never closes on an
 * outside press, so backdrop handling stays with the caller.
 */

export type AlertDialogRootProps = Omit<BaseAlertDialog.Root.Props, 'open' | 'onOpenChange'> & {
  open: NonNullable<BaseAlertDialog.Root.Props['open']>
  onOpenChange: NonNullable<BaseAlertDialog.Root.Props['onOpenChange']>
}

export function AlertDialogRoot(props: AlertDialogRootProps): React.JSX.Element {
  return <BaseAlertDialog.Root {...props} />
}

export type AlertDialogPortalProps = BaseAlertDialog.Portal.Props

export function AlertDialogPortal(props: AlertDialogPortalProps): React.JSX.Element {
  return <BaseAlertDialog.Portal {...props} />
}

export type AlertDialogBackdropProps = Omit<
  BaseAlertDialog.Backdrop.Props,
  'className' | 'forceRender'
> & {
  className?: DialogClassName
  /** Render the backdrop even when the alert dialog is nested inside another one. */
  forceRender?: BaseAlertDialog.Backdrop.Props['forceRender']
}

export function AlertDialogBackdrop({
  className,
  ...props
}: AlertDialogBackdropProps): React.JSX.Element {
  return <BaseAlertDialog.Backdrop {...props} className={className} />
}

export type AlertDialogViewportProps = Omit<BaseAlertDialog.Viewport.Props, 'className'> & {
  className?: DialogClassName
}

export function AlertDialogViewport({
  className,
  ...props
}: AlertDialogViewportProps): React.JSX.Element {
  return <BaseAlertDialog.Viewport {...props} className={className} />
}

export type AlertDialogPopupProps = Omit<BaseAlertDialog.Popup.Props, 'className' | 'render'> & {
  className?: DialogClassName
}

export function AlertDialogPopup({
  className,
  ...props
}: AlertDialogPopupProps): React.JSX.Element {
  return <BaseAlertDialog.Popup {...props} className={className} />
}

export type AlertDialogTitleProps = Omit<BaseAlertDialog.Title.Props, 'render'>

export function AlertDialogTitle(props: AlertDialogTitleProps): React.JSX.Element {
  return <BaseAlertDialog.Title {...props} render={<h3 />} />
}

export type AlertDialogDescriptionProps = BaseAlertDialog.Description.Props

export function AlertDialogDescription(props: AlertDialogDescriptionProps): React.JSX.Element {
  return <BaseAlertDialog.Description {...props} />
}

export type AlertDialogCloseProps = BaseAlertDialog.Close.Props

export function AlertDialogClose(props: AlertDialogCloseProps): React.JSX.Element {
  return <BaseAlertDialog.Close {...props} />
}

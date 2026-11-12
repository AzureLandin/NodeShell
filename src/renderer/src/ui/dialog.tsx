import { Dialog as BaseDialog } from '@base-ui/react/dialog'
import './dialog.css'

// Derive compatibility types from the installed library. Application close
// policy belongs to ModalShell; vendor event unions are not copied here.
export type DialogRootProps = BaseDialog.Root.Props
export type DialogPopupProps = BaseDialog.Popup.Props
export type DialogChangeDetails = Parameters<NonNullable<DialogRootProps['onOpenChange']>>[1]
export type DialogCloseReason = DialogChangeDetails['reason']
export type DialogFocusTarget = DialogPopupProps['initialFocus']
export type DialogPartState = Pick<BaseDialog.Popup.State, 'open' | 'transitionStatus'>
export type DialogTransitionStatus = DialogPartState['transitionStatus']
export type DialogClassName = string | ((state: DialogPartState) => string | undefined) | undefined

export function DialogRoot(props: DialogRootProps): React.JSX.Element {
  return <BaseDialog.Root {...props} />
}

export type DialogPortalProps = BaseDialog.Portal.Props

export function DialogPortal(props: DialogPortalProps): React.JSX.Element {
  return <BaseDialog.Portal {...props} />
}

export type DialogBackdropProps = BaseDialog.Backdrop.Props

export function DialogBackdrop(props: DialogBackdropProps): React.JSX.Element {
  return <BaseDialog.Backdrop {...props} />
}

export type DialogViewportProps = BaseDialog.Viewport.Props

export function DialogViewport(props: DialogViewportProps): React.JSX.Element {
  return <BaseDialog.Viewport {...props} />
}

export function DialogPopup(props: DialogPopupProps): React.JSX.Element {
  return <BaseDialog.Popup {...props} />
}

export type DialogTitleProps = BaseDialog.Title.Props

export function DialogTitle(props: DialogTitleProps): React.JSX.Element {
  return <BaseDialog.Title {...props} render={<h3 />} />
}

export type DialogDescriptionProps = BaseDialog.Description.Props

export function DialogDescription(props: DialogDescriptionProps): React.JSX.Element {
  return <BaseDialog.Description {...props} />
}

export type DialogCloseProps = BaseDialog.Close.Props

export function DialogClose(props: DialogCloseProps): React.JSX.Element {
  return <BaseDialog.Close {...props} />
}

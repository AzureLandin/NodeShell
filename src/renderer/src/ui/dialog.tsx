import { Dialog as BaseDialog } from '@base-ui/react/dialog'
import type { MouseEventHandler, PointerEventHandler, ReactNode, Ref, RefObject } from 'react'
import './dialog.css'

/**
 * Thin wrappers around `@base-ui/react/dialog`.
 *
 * Only the parts the app needs are re-exported, and every prop is described
 * with app-local types so Base UI types never leak into the component tree.
 */

/** Interaction that opened or closed a dialog. */
export type DialogInteractionType = 'mouse' | 'touch' | 'pen' | 'keyboard' | ''

/** Transition phase Base UI reports while a dialog enters or leaves. */
export type DialogTransitionStatus = 'starting' | 'ending' | 'idle' | undefined

/** Minimal view of a Base UI part state — enough to derive CSS classes. */
export interface DialogPartState {
  open: boolean
  transitionStatus: DialogTransitionStatus
}

/** CSS class of a part: a literal string or a function of the part state. */
export type DialogClassName = string | ((state: DialogPartState) => string | undefined)

/** Element (or decision) Base UI should move focus to. */
export type DialogFocusTarget =
  | boolean
  | RefObject<HTMLElement | null>
  | ((interaction: DialogInteractionType) => boolean | HTMLElement | null | void)

/** Why a dialog asked to close. */
export type DialogCloseReason =
  | 'trigger-press'
  | 'outside-press'
  | 'escape-key'
  | 'close-press'
  | 'focus-out'
  | 'imperative-action'
  | 'none'

/** Narrow view of Base UI's change details. */
export interface DialogChangeDetails {
  /** Reason the dialog wants to close. */
  reason: DialogCloseReason
  /** Rejects the close request and keeps the dialog open. */
  cancel: () => void
  /** Keeps the originating event propagating (e.g. to window-level handlers). */
  allowPropagation: () => void
}

export interface DialogRootProps {
  /** Whether the dialog is open. */
  open: boolean
  /** Called whenever the dialog wants to open or close. */
  onOpenChange: (open: boolean, details: DialogChangeDetails) => void
  /** Called once the enter/exit transition of the dialog finished. */
  onOpenChangeComplete?: (open: boolean) => void
  /** Ignore backdrop presses (outside clicks). */
  disablePointerDismissal?: boolean
  children?: ReactNode
}

export function DialogRoot({
  open,
  onOpenChange,
  onOpenChangeComplete,
  disablePointerDismissal = false,
  children
}: DialogRootProps): React.JSX.Element {
  return (
    <BaseDialog.Root
      open={open}
      onOpenChange={onOpenChange}
      onOpenChangeComplete={onOpenChangeComplete}
      disablePointerDismissal={disablePointerDismissal}
    >
      {children}
    </BaseDialog.Root>
  )
}

export interface DialogPortalProps {
  children?: ReactNode
}

export function DialogPortal({ children }: DialogPortalProps): React.JSX.Element {
  return <BaseDialog.Portal>{children}</BaseDialog.Portal>
}

export interface DialogBackdropProps {
  className?: DialogClassName
  /** Render the backdrop even when the dialog is nested inside another one. */
  forceRender?: boolean
  onClick?: MouseEventHandler<HTMLDivElement>
  onPointerDown?: PointerEventHandler<HTMLDivElement>
  children?: ReactNode
}

export function DialogBackdrop({
  className,
  forceRender = false,
  onClick,
  onPointerDown,
  children
}: DialogBackdropProps): React.JSX.Element {
  return (
    <BaseDialog.Backdrop
      className={className}
      forceRender={forceRender}
      onClick={onClick}
      onPointerDown={onPointerDown}
    >
      {children}
    </BaseDialog.Backdrop>
  )
}

export interface DialogViewportProps {
  className?: DialogClassName
  children?: ReactNode
}

export function DialogViewport({ className, children }: DialogViewportProps): React.JSX.Element {
  return <BaseDialog.Viewport className={className}>{children}</BaseDialog.Viewport>
}

export interface DialogPopupProps {
  className?: DialogClassName
  children?: ReactNode
  id?: string
  ref?: Ref<HTMLDivElement>
  'aria-labelledby'?: string
  'aria-modal'?: boolean | 'true' | 'false'
  /** Element focused when the dialog opens. Defaults to the first focusable child. */
  initialFocus?: DialogFocusTarget
  /** Element focused when the dialog closes. Defaults to the previously focused element. */
  finalFocus?: DialogFocusTarget
}

export function DialogPopup({
  className,
  children,
  id,
  ref,
  initialFocus,
  finalFocus,
  'aria-labelledby': ariaLabelledBy,
  'aria-modal': ariaModal
}: DialogPopupProps): React.JSX.Element {
  return (
    <BaseDialog.Popup
      className={className}
      id={id}
      ref={ref}
      initialFocus={initialFocus}
      finalFocus={finalFocus}
      aria-labelledby={ariaLabelledBy}
      aria-modal={ariaModal}
    >
      {children}
    </BaseDialog.Popup>
  )
}

export interface DialogTitleProps {
  className?: string
  id?: string
  children?: ReactNode
}

export function DialogTitle({ className, id, children }: DialogTitleProps): React.JSX.Element {
  return (
    <BaseDialog.Title className={className} id={id} render={<h3 />}>
      {children}
    </BaseDialog.Title>
  )
}

export interface DialogDescriptionProps {
  className?: string
  id?: string
  children?: ReactNode
}

export function DialogDescription({
  className,
  id,
  children
}: DialogDescriptionProps): React.JSX.Element {
  return (
    <BaseDialog.Description className={className} id={id}>
      {children}
    </BaseDialog.Description>
  )
}

export interface DialogCloseProps {
  className?: string
  children?: ReactNode
  onClick?: MouseEventHandler<HTMLButtonElement>
}

export function DialogClose({ className, children, onClick }: DialogCloseProps): React.JSX.Element {
  return (
    <BaseDialog.Close className={className} onClick={onClick}>
      {children}
    </BaseDialog.Close>
  )
}

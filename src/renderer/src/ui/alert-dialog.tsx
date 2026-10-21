import { AlertDialog as BaseAlertDialog } from '@base-ui/react/alert-dialog'
import type { MouseEventHandler, PointerEventHandler, ReactNode, Ref } from 'react'
import type { DialogChangeDetails, DialogClassName, DialogFocusTarget } from './dialog'
import './dialog.css'

/**
 * Thin wrappers around `@base-ui/react/alert-dialog`.
 *
 * An alert dialog asks the user to confirm an action: it never closes on an
 * outside press, so backdrop handling stays with the caller.
 */

export interface AlertDialogRootProps {
  /** Whether the alert dialog is open. */
  open: boolean
  /** Called whenever the alert dialog wants to open or close. */
  onOpenChange: (open: boolean, details: DialogChangeDetails) => void
  /** Called once the enter/exit transition of the alert dialog finished. */
  onOpenChangeComplete?: (open: boolean) => void
  children?: ReactNode
}

export function AlertDialogRoot({
  open,
  onOpenChange,
  onOpenChangeComplete,
  children
}: AlertDialogRootProps): React.JSX.Element {
  return (
    <BaseAlertDialog.Root
      open={open}
      onOpenChange={onOpenChange}
      onOpenChangeComplete={onOpenChangeComplete}
    >
      {children}
    </BaseAlertDialog.Root>
  )
}

export interface AlertDialogPortalProps {
  children?: ReactNode
}

export function AlertDialogPortal({ children }: AlertDialogPortalProps): React.JSX.Element {
  return <BaseAlertDialog.Portal>{children}</BaseAlertDialog.Portal>
}

export interface AlertDialogBackdropProps {
  className?: DialogClassName
  /** Render the backdrop even when the alert dialog is nested inside another one. */
  forceRender?: boolean
  onClick?: MouseEventHandler<HTMLDivElement>
  onPointerDown?: PointerEventHandler<HTMLDivElement>
  children?: ReactNode
}

export function AlertDialogBackdrop({
  className,
  forceRender = false,
  onClick,
  onPointerDown,
  children
}: AlertDialogBackdropProps): React.JSX.Element {
  return (
    <BaseAlertDialog.Backdrop
      className={className}
      forceRender={forceRender}
      onClick={onClick}
      onPointerDown={onPointerDown}
    >
      {children}
    </BaseAlertDialog.Backdrop>
  )
}

export interface AlertDialogViewportProps {
  className?: DialogClassName
  children?: ReactNode
}

export function AlertDialogViewport({
  className,
  children
}: AlertDialogViewportProps): React.JSX.Element {
  return <BaseAlertDialog.Viewport className={className}>{children}</BaseAlertDialog.Viewport>
}

export interface AlertDialogPopupProps {
  className?: DialogClassName
  children?: ReactNode
  id?: string
  ref?: Ref<HTMLDivElement>
  'aria-labelledby'?: string
  'aria-modal'?: boolean | 'true' | 'false'
  /** Element focused when the alert dialog opens. Defaults to the first focusable child. */
  initialFocus?: DialogFocusTarget
  /** Element focused when the alert dialog closes. Defaults to the previously focused element. */
  finalFocus?: DialogFocusTarget
}

export function AlertDialogPopup({
  className,
  children,
  id,
  ref,
  initialFocus,
  finalFocus,
  'aria-labelledby': ariaLabelledBy,
  'aria-modal': ariaModal
}: AlertDialogPopupProps): React.JSX.Element {
  return (
    <BaseAlertDialog.Popup
      className={className}
      id={id}
      ref={ref}
      initialFocus={initialFocus}
      finalFocus={finalFocus}
      aria-labelledby={ariaLabelledBy}
      aria-modal={ariaModal}
    >
      {children}
    </BaseAlertDialog.Popup>
  )
}

export interface AlertDialogTitleProps {
  className?: string
  id?: string
  children?: ReactNode
}

export function AlertDialogTitle({
  className,
  id,
  children
}: AlertDialogTitleProps): React.JSX.Element {
  return (
    <BaseAlertDialog.Title className={className} id={id} render={<h3 />}>
      {children}
    </BaseAlertDialog.Title>
  )
}

export interface AlertDialogDescriptionProps {
  className?: string
  id?: string
  children?: ReactNode
}

export function AlertDialogDescription({
  className,
  id,
  children
}: AlertDialogDescriptionProps): React.JSX.Element {
  return (
    <BaseAlertDialog.Description className={className} id={id}>
      {children}
    </BaseAlertDialog.Description>
  )
}

export interface AlertDialogCloseProps {
  className?: string
  children?: ReactNode
  onClick?: MouseEventHandler<HTMLButtonElement>
}

export function AlertDialogClose({
  className,
  children,
  onClick
}: AlertDialogCloseProps): React.JSX.Element {
  return (
    <BaseAlertDialog.Close className={className} onClick={onClick}>
      {children}
    </BaseAlertDialog.Close>
  )
}

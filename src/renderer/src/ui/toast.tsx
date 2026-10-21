import type { ReactNode } from 'react'
import { Toast } from '@base-ui/react/toast'
import type { ToastObject } from '@base-ui/react/toast'
import './ui.css'

/**
 * Thin wrappers around Base UI's toast primitives.
 *
 * The provider owns the toast list, the viewport owns the position of the
 * stack and the surface renders a single toast. Class names stay the ones the
 * app already uses, so `App.css` keeps styling the toast exactly as before.
 */

/** Payload attached to a toast; the app does not use one today. */
export type ToastData = Record<string, unknown>

/** A toast as tracked by the Base UI toast manager. */
export type ToastEntry = ToastObject<ToastData>

export interface ToastProviderProps {
  /**
   * Auto dismiss delay in milliseconds. `0` keeps a toast on screen until it is
   * dismissed explicitly.
   */
  timeout?: number
  /** Maximum number of toasts kept on screen at the same time. */
  limit?: number
  children: ReactNode
}

/** Provides the toast context the viewport and the toasts read from. */
export function ToastProvider({
  timeout = 0,
  limit = 3,
  children
}: ToastProviderProps): React.JSX.Element {
  return (
    <Toast.Provider timeout={timeout} limit={limit}>
      {children}
    </Toast.Provider>
  )
}

export interface ToastViewportProps {
  /** Class name for the viewport element. */
  className?: string
  children: ReactNode
}

/** Portal holding the stack of toasts. */
export function ToastViewport({ className, children }: ToastViewportProps): React.JSX.Element {
  return (
    <Toast.Portal>
      <Toast.Viewport className={className} data-bui-toast-viewport="">
        {children}
      </Toast.Viewport>
    </Toast.Portal>
  )
}

export interface ToastSurfaceProps {
  /** The toast to render. */
  toast: ToastEntry
  /** Class name of the toast surface. */
  className: string
  /** Class name of the element holding the toast text. */
  titleClassName: string
  /** Class name of the dismiss button. */
  closeClassName: string
  /** Accessible label of the dismiss button. */
  closeLabel: string
}

/** A single toast: its text plus the dismiss button. */
export function ToastSurface({
  toast,
  className,
  titleClassName,
  closeClassName,
  closeLabel
}: ToastSurfaceProps): React.JSX.Element {
  return (
    <Toast.Root
      toast={toast}
      className={className}
      role="status"
      aria-modal={undefined}
      data-bui-toast=""
    >
      <Toast.Title className={titleClassName} render={<span />}>
        {toast.title}
      </Toast.Title>
      {/*
        Base UI hides the dismiss button from assistive technology until the
        toast is expanded. The app shows a single, always actionable toast, so
        the button stays exposed.
      */}
      <Toast.Close className={closeClassName} aria-label={closeLabel} aria-hidden={undefined}>
        ×
      </Toast.Close>
    </Toast.Root>
  )
}

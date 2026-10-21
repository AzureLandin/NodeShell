import { createContext, useCallback, useContext, useLayoutEffect, useRef, useState } from 'react'
import {
  DialogBackdrop,
  DialogPopup,
  DialogPortal,
  DialogRoot,
  DialogViewport,
  type DialogChangeDetails,
  type DialogPartState
} from '../ui/dialog'

const ModalCloseContext = createContext<() => void>(() => {})

export function useModalClose(): () => void {
  return useContext(ModalCloseContext)
}

export type ModalMotion = 'default' | 'simple' | 'none'

interface ModalShellProps {
  onClose: () => void
  children: React.ReactNode
  dialogClassName?: string
  /** Extra class for the dimmed overlay (e.g. a higher stacking order). */
  overlayClassName?: string
  labelledBy?: string
  /** Default true. Set false while a nested form owns Escape, or during busy connect. */
  closeOnEscape?: boolean
  /** Default true. Password modal keeps this false (match previous behavior). */
  closeOnOverlayClick?: boolean
  /** Modal animation variant: 'default' (scale+fade), 'simple' (fade only), 'none' (instant). */
  motion?: ModalMotion
  /** Element focused when the modal opens. Defaults to the first focusable child. */
  initialFocus?: React.RefObject<HTMLElement | null>
}

function prefersReducedMotion(): boolean {
  return window.matchMedia('(prefers-reduced-motion: reduce)').matches
}

/**
 * Maps the Base UI transition phase onto the legacy visual states that
 * App.css animates: `is-open` while the dialog is fully open, `is-closing`
 * while it plays the exit transition and nothing while it enters.
 */
function transitionClass(state: DialogPartState): string {
  if (state.transitionStatus === 'ending') return 'is-closing'
  if (state.transitionStatus === 'starting') return ''
  return 'is-open'
}

export function ModalShell({
  onClose,
  children,
  dialogClassName = '',
  overlayClassName = '',
  labelledBy,
  closeOnEscape = true,
  closeOnOverlayClick = true,
  motion = 'default',
  initialFocus
}: ModalShellProps): React.JSX.Element {
  // Reduced motion users get the dialog without an enter transition, everyone
  // else mounts closed and opens on the next layout tick so Base UI runs the
  // `data-starting-style` phase.
  const [open, setOpen] = useState(() => prefersReducedMotion())
  const onCloseRef = useRef(onClose)
  const closeOnEscapeRef = useRef(closeOnEscape)
  const closeOnOverlayClickRef = useRef(closeOnOverlayClick)
  onCloseRef.current = onClose
  closeOnEscapeRef.current = closeOnEscape
  closeOnOverlayClickRef.current = closeOnOverlayClick

  useLayoutEffect(() => {
    setOpen(true)
  }, [])

  const requestClose = useCallback(() => {
    setOpen(false)
  }, [])

  const handleOpenChange = useCallback((nextOpen: boolean, details: DialogChangeDetails) => {
    if (nextOpen) {
      setOpen(true)
      return
    }
    if (details.reason === 'escape-key' && !closeOnEscapeRef.current) {
      // A nested form owns Escape (SFTP editor, host picker form): keep the
      // key event alive so the window-level handler still sees it.
      details.cancel()
      details.allowPropagation()
      return
    }
    if (details.reason === 'outside-press' && !closeOnOverlayClickRef.current) {
      details.cancel()
      return
    }
    setOpen(false)
  }, [])

  const handleOpenChangeComplete = useCallback((nextOpen: boolean) => {
    // The exit transition finished — now hand control back to the owner.
    if (!nextOpen) onCloseRef.current()
  }, [])

  const overlayClass = useCallback(
    (state: DialogPartState) =>
      [
        'modal-overlay',
        overlayClassName,
        motion === 'default' ? 'modal-overlay--animated' : '',
        motion === 'simple' ? 'modal-overlay--simple' : '',
        transitionClass(state)
      ]
        .filter(Boolean)
        .join(' '),
    [motion, overlayClassName]
  )

  const dialogClass = useCallback(
    (state: DialogPartState) =>
      [
        'modal',
        motion === 'default' ? 'modal--animated' : '',
        motion === 'simple' ? 'modal-motion-simple' : '',
        dialogClassName,
        transitionClass(state)
      ]
        .filter(Boolean)
        .join(' '),
    [dialogClassName, motion]
  )

  return (
    <ModalCloseContext.Provider value={requestClose}>
      <DialogRoot
        open={open}
        onOpenChange={handleOpenChange}
        onOpenChangeComplete={handleOpenChangeComplete}
        disablePointerDismissal={!closeOnOverlayClick}
      >
        <DialogPortal>
          <DialogBackdrop className={overlayClass} forceRender>
            <DialogViewport className="modal-viewport">
              <DialogPopup
                className={dialogClass}
                aria-labelledby={labelledBy}
                aria-modal="true"
                initialFocus={initialFocus}
              >
                {children}
              </DialogPopup>
            </DialogViewport>
          </DialogBackdrop>
        </DialogPortal>
      </DialogRoot>
    </ModalCloseContext.Provider>
  )
}

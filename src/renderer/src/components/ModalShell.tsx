import { createContext, useCallback, useContext, useEffect, useRef, useState } from 'react'
import { createPortal } from 'react-dom'

const CLOSE_FALLBACK_MS = 400
const CLOSE_FALLBACK_SIMPLE_MS = 180

type Phase = 'preenter' | 'open' | 'closing'

export type ModalMotion = 'default' | 'simple' | 'none'

const ModalCloseContext = createContext<() => void>(() => {})

export function useModalClose(): () => void {
  return useContext(ModalCloseContext)
}

interface ModalShellProps {
  onClose: () => void
  children: React.ReactNode
  dialogClassName?: string
  labelledBy?: string
  /** Default true. Set false while a nested form owns Escape, or during busy connect. */
  closeOnEscape?: boolean
  /** Default true. Password modal keeps this false (match previous behavior). */
  closeOnOverlayClick?: boolean
  /** Modal animation variant: 'default' (scale+fade), 'simple' (fade only), 'none' (instant). */
  motion?: ModalMotion
}

function prefersReducedMotion(): boolean {
  return window.matchMedia('(prefers-reduced-motion: reduce)').matches
}

export function ModalShell({
  onClose,
  children,
  dialogClassName = '',
  labelledBy,
  closeOnEscape = true,
  closeOnOverlayClick = true,
  motion = 'default'
}: ModalShellProps): React.JSX.Element {
  const isNoneMotion = motion === 'none' || prefersReducedMotion()
  const [phase, setPhase] = useState<Phase>(() => (isNoneMotion ? 'open' : 'preenter'))
  const closingRef = useRef(false)
  const onCloseRef = useRef(onClose)
  const timeoutRef = useRef<number | null>(null)
  const closeOnEscapeRef = useRef(closeOnEscape)
  onCloseRef.current = onClose
  closeOnEscapeRef.current = closeOnEscape

  useEffect(() => {
    if (isNoneMotion) {
      setPhase('open')
      return
    }
    const id = requestAnimationFrame(() => {
      requestAnimationFrame(() => setPhase('open'))
    })
    return () => cancelAnimationFrame(id)
  }, [isNoneMotion])

  const finishClose = useCallback(() => {
    if (!closingRef.current) return
    closingRef.current = false
    if (timeoutRef.current != null) {
      window.clearTimeout(timeoutRef.current)
      timeoutRef.current = null
    }
    onCloseRef.current()
  }, [])

  const requestClose = useCallback(() => {
    if (closingRef.current) return
    closingRef.current = true
    if (isNoneMotion) {
      onCloseRef.current()
      closingRef.current = false
      return
    }
    setPhase('closing')
    const fallback = motion === 'simple' ? CLOSE_FALLBACK_SIMPLE_MS : CLOSE_FALLBACK_MS
    timeoutRef.current = window.setTimeout(finishClose, fallback)
  }, [finishClose, isNoneMotion, motion])

  useEffect(() => {
    const onKey = (e: KeyboardEvent): void => {
      if (e.key === 'Escape' && closeOnEscapeRef.current) requestClose()
    }
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  }, [requestClose])

  useEffect(() => {
    return () => {
      if (timeoutRef.current != null) window.clearTimeout(timeoutRef.current)
    }
  }, [])

  const onDialogTransitionEnd = (e: React.TransitionEvent<HTMLDivElement>): void => {
    if (e.target !== e.currentTarget) return
    if (phase !== 'closing') return
    if (e.propertyName !== 'opacity') return
    finishClose()
  }

  const overlayClass = [
    'modal-overlay',
    motion === 'default' ? 'modal-overlay--animated' : '',
    motion === 'simple' ? 'modal-overlay--simple' : '',
    phase === 'open' ? 'is-open' : '',
    phase === 'closing' ? 'is-closing' : ''
  ]
    .filter(Boolean)
    .join(' ')

  const dialogClass = [
    'modal',
    motion === 'default' ? 'modal--animated' : '',
    motion === 'simple' ? 'modal-motion-simple' : '',
    dialogClassName,
    phase === 'open' ? 'is-open' : '',
    phase === 'closing' ? 'is-closing' : ''
  ]
    .filter(Boolean)
    .join(' ')

  const overlayPointerDownRef = useRef(false)

  const onOverlayPointerDown = (e: React.PointerEvent<HTMLDivElement>): void => {
    // Only count presses that start on the dimmed backdrop, not the dialog.
    overlayPointerDownRef.current = e.target === e.currentTarget
  }

  const onOverlayClick = (e: React.MouseEvent<HTMLDivElement>): void => {
    if (!closeOnOverlayClick) return
    // Ignore click if the gesture started inside the dialog (e.g. text selection
    // drag that ends on the backdrop) — that used to close the host picker.
    if (!overlayPointerDownRef.current) return
    if (e.target !== e.currentTarget) return
    requestClose()
  }

  return createPortal(
    <ModalCloseContext.Provider value={requestClose}>
      <div
        className={overlayClass}
        role="presentation"
        onPointerDown={onOverlayPointerDown}
        onClick={onOverlayClick}
      >
        <div
          className={dialogClass}
          role="dialog"
          aria-modal="true"
          aria-labelledby={labelledBy}
          onClick={(e) => e.stopPropagation()}
          onTransitionEnd={onDialogTransitionEnd}
        >
          {children}
        </div>
      </div>
    </ModalCloseContext.Provider>,
    document.body
  )
}

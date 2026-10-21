import { useCallback, useRef, useState } from 'react'
import { useTranslation } from 'react-i18next'
import {
  AlertDialogBackdrop,
  AlertDialogClose,
  AlertDialogDescription,
  AlertDialogPopup,
  AlertDialogPortal,
  AlertDialogRoot,
  AlertDialogTitle,
  AlertDialogViewport
} from '../ui/alert-dialog'

interface ConfirmModalProps {
  title: string
  message: string
  confirmLabel?: string
  cancelLabel?: string
  onConfirm: () => void
  onCancel: () => void
}

/** Instant confirm — no enter/exit motion (high-frequency actions). */
export function ConfirmModal({
  title,
  message,
  confirmLabel,
  cancelLabel,
  onConfirm,
  onCancel
}: ConfirmModalProps): React.JSX.Element {
  const { t } = useTranslation()
  const [open, setOpen] = useState(true)
  const popupRef = useRef<HTMLDivElement | null>(null)
  const confirmRef = useRef<HTMLButtonElement | null>(null)
  const overlayPointerDownRef = useRef(false)

  const handleOpenChange = useCallback(
    (nextOpen: boolean) => {
      setOpen(nextOpen)
      if (!nextOpen) onCancel()
    },
    [onCancel]
  )

  const onBackdropPointerDown = useCallback((e: React.PointerEvent<HTMLDivElement>) => {
    // Only presses that start on the dimmed backdrop count, not presses that
    // start inside the dialog and end on the backdrop.
    overlayPointerDownRef.current = !(popupRef.current?.contains(e.target as Node) ?? false)
  }, [])

  const onBackdropClick = useCallback(() => {
    if (!overlayPointerDownRef.current) return
    overlayPointerDownRef.current = false
    onCancel()
  }, [onCancel])

  return (
    <AlertDialogRoot open={open} onOpenChange={handleOpenChange}>
      <AlertDialogPortal>
        <AlertDialogBackdrop
          className="modal-overlay confirm-modal-overlay"
          forceRender
          onPointerDown={onBackdropPointerDown}
          onClick={onBackdropClick}
        >
          <AlertDialogViewport className="modal-viewport">
            <AlertDialogPopup
              ref={popupRef}
              className="modal confirm-modal"
              aria-labelledby="confirm-modal-title"
              aria-modal="true"
              initialFocus={confirmRef}
            >
              <AlertDialogTitle id="confirm-modal-title" className="modal-title">
                {title}
              </AlertDialogTitle>
              <AlertDialogDescription className="confirm-modal-message">
                {message}
              </AlertDialogDescription>
              <div className="form-actions">
                <AlertDialogClose className="btn-secondary">
                  {cancelLabel ?? t('common.cancel')}
                </AlertDialogClose>
                <button
                  type="button"
                  className="btn-primary"
                  ref={confirmRef}
                  onClick={onConfirm}
                  autoFocus
                >
                  {confirmLabel ?? t('common.confirm')}
                </button>
              </div>
            </AlertDialogPopup>
          </AlertDialogViewport>
        </AlertDialogBackdrop>
      </AlertDialogPortal>
    </AlertDialogRoot>
  )
}

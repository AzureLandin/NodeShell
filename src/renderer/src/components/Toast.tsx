import { useEffect, useRef } from 'react'
import { useTranslation } from 'react-i18next'
import { Toast as BaseToast } from '@base-ui/react/toast'
import { ToastProvider, ToastSurface, ToastViewport, type ToastData } from '../ui/toast'

interface ToastProps {
  message: string | null
  onClose: () => void
  /**
   * Optional variant, forwarded to Base UI as the toast type. It ends up as a
   * `data-type` attribute on the toast surface.
   */
  tone?: string
  /**
   * Auto dismiss delay in milliseconds. `0` keeps the toast on screen until it
   * is dismissed, which is what the app has always done.
   */
  timeout?: number
}

/** Single toast id: a new message replaces the previous one in place. */
const TOAST_ID = 'app-toast'

/**
 * Single toast surface of the app.
 *
 * The component owns the Base UI toast provider, so mounting it once (where
 * the toast used to be rendered) is enough; `App.tsx` keeps using the same
 * `message` / `onClose` contract.
 */
export function Toast({ message, onClose, tone, timeout = 0 }: ToastProps): React.JSX.Element {
  return (
    <ToastProvider timeout={timeout}>
      <ToastRegion message={message} onClose={onClose} tone={tone} timeout={timeout} />
    </ToastProvider>
  )
}

function ToastRegion({
  message,
  onClose,
  tone,
  timeout
}: {
  message: string | null
  onClose: () => void
  tone?: string
  timeout: number
}): React.JSX.Element {
  const { t } = useTranslation()
  const { add, close, toasts } = BaseToast.useToastManager<ToastData>()

  // Keep the latest callback without re-running the effect on every render:
  // `App.tsx` passes a new arrow function each time.
  const onCloseRef = useRef(onClose)
  useEffect(() => {
    onCloseRef.current = onClose
  }, [onClose])

  // A single toast id keeps the "one message at a time" contract: a new message
  // replaces the previous one in place instead of stacking a new toast.
  useEffect(() => {
    if (!message) {
      close(TOAST_ID)
      return
    }
    add({
      id: TOAST_ID,
      title: message,
      type: tone,
      timeout,
      onClose: () => onCloseRef.current()
    })
  }, [message, tone, timeout, add, close])

  return (
    <ToastViewport>
      {toasts.map((toast) => (
        <ToastSurface
          key={toast.id}
          toast={toast}
          className="toast"
          titleClassName="toast-message"
          closeClassName="toast-dismiss"
          closeLabel={t('common.dismiss')}
        />
      ))}
    </ToastViewport>
  )
}

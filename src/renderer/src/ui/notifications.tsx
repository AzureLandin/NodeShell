import { useCallback, useLayoutEffect, useRef, type ReactNode } from 'react'
import { useTranslation } from 'react-i18next'
import { NotificationContext, type Notify } from './notification-context'
import { ToastProvider, ToastSurface, ToastViewport } from './toast'
import { useToastManager } from './use-toast-manager'

const NOTIFICATION_ID = 'app-notification'

function NotificationScope({ children }: { children: ReactNode }): React.JSX.Element {
  const { t } = useTranslation()
  const { add, toasts } = useToastManager()
  const mountedRef = useRef(true)

  useLayoutEffect(() => {
    mountedRef.current = true
    return () => {
      mountedRef.current = false
    }
  }, [])

  const notify = useCallback<Notify>(
    (message, options = {}) => {
      if (!mountedRef.current || !message) return
      // Base UI upserts this ID and refreshes its timer. Repeating a message
      // also replaces it; a closed notification can be published again.
      add({
        id: NOTIFICATION_ID,
        title: message,
        type: options.tone,
        timeout: options.timeout ?? 0
      })
    },
    [add]
  )

  return (
    <NotificationContext.Provider value={notify}>
      {children}
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
    </NotificationContext.Provider>
  )
}

/** Install once at the root. Base UI owns the single notification and timers. */
export function NotificationProvider({ children }: { children: ReactNode }): React.JSX.Element {
  return (
    <ToastProvider timeout={0} limit={1}>
      <NotificationScope>{children}</NotificationScope>
    </ToastProvider>
  )
}

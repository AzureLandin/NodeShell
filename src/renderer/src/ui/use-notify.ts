import { useContext } from 'react'
import { NotificationContext, type Notify } from './notification-context'

export type { NotificationOptions } from './notification-context'

/** Application notification entry; the provider owns all presentation state. */
export function useNotify(): Notify {
  const notify = useContext(NotificationContext)
  if (!notify) throw new Error('useNotify must be used within NotificationProvider')
  return notify
}

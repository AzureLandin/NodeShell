import { Toast } from '@base-ui/react/toast'
import type { ToastData } from './toast'

/** Keep the vendor hook behind the same boundary as its visual primitives. */
export function useToastManager(): ReturnType<typeof Toast.useToastManager<ToastData>> {
  return Toast.useToastManager<ToastData>()
}

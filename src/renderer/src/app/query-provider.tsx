import { QueryClientProvider } from '@tanstack/react-query'
import { useEffect, useRef, useState, type ReactNode } from 'react'
import { createAppQueryClient } from './query-client'

/** One in-memory client per mounted application; no global or persisted cache. */
export function AppQueryProvider({ children }: { children: ReactNode }): React.JSX.Element {
  const [client] = useState(createAppQueryClient)
  const active = useRef(false)

  useEffect(() => {
    active.current = true
    return () => {
      active.current = false
      // StrictMode immediately replays setup; dispose only after an actual unmount.
      queueMicrotask(() => {
        if (!active.current) client.clear()
      })
    }
  }, [client])

  return <QueryClientProvider client={client}>{children}</QueryClientProvider>
}

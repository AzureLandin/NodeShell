import { useCallback, useLayoutEffect, useRef, useState } from 'react'
import type { Confirmation } from '../ui/confirmation'

interface PendingConfirmation extends Confirmation {
  id: number
  resolve: (accepted: boolean) => void
}

/** FIFO ownership: every caller settles once, including on application disposal. */
export function useConfirmationQueue(): {
  current: PendingConfirmation | null
  ask: (request: Confirmation) => Promise<boolean>
  settle: (id: number, accepted: boolean) => void
} {
  const queue = useRef<PendingConfirmation[]>([])
  const nextId = useRef(0)
  const active = useRef(false)
  const [current, setCurrent] = useState<PendingConfirmation | null>(null)

  useLayoutEffect(() => {
    active.current = true
    const pendingQueue = queue.current
    return () => {
      active.current = false
      const pending = pendingQueue.splice(0)
      pending.forEach((request) => request.resolve(false))
    }
  }, [])

  const ask = useCallback((request: Confirmation): Promise<boolean> => {
    if (!active.current) return Promise.resolve(false)
    return new Promise((resolve) => {
      const pending = { ...request, id: ++nextId.current, resolve }
      queue.current.push(pending)
      if (queue.current.length === 1) setCurrent(pending)
    })
  }, [])

  const settle = useCallback((id: number, accepted: boolean): void => {
    const first = queue.current[0]
    if (!active.current || first?.id !== id) return
    queue.current.shift()
    setCurrent(queue.current[0] ?? null)
    first.resolve(accepted)
  }, [])

  return { current, ask, settle }
}

import { useEffect, useRef, useState } from 'react'
import { useTranslation } from 'react-i18next'
import type { PermissionAskEvent, PermissionDecision } from '../../../../shared/types'
import { parseIpcThrownError } from '../../../../shared/ipc-error'
import { useNotify } from '../../ui/use-notify'

export function usePermissionQueue(onAgentRequest: () => void): {
  current: PermissionAskEvent | null
  decide: (id: string, decision: PermissionDecision) => void
} {
  const { t } = useTranslation()
  const notify = useNotify()
  const [queue, setQueue] = useState<PermissionAskEvent[]>([])
  const queueRef = useRef(queue)
  queueRef.current = queue
  const onAgentRequestRef = useRef(onAgentRequest)
  onAgentRequestRef.current = onAgentRequest
  const active = useRef(false)
  const pendingDecisions = useRef(new Set<string>())
  const lifetime = useRef(0)

  useEffect(() => {
    active.current = true
    lifetime.current += 1
    const offAsk = window.api.permission.onAsk((request) => {
      if (queueRef.current.some((item) => item.id === request.id)) return
      queueRef.current = [...queueRef.current, request]
      setQueue(queueRef.current)
      if (request.source === 'agent') onAgentRequestRef.current()
    })
    const offClosed = window.api.permission.onClosed(({ id }) => {
      queueRef.current = queueRef.current.filter((request) => request.id !== id)
      setQueue(queueRef.current)
    })
    return () => {
      active.current = false
      lifetime.current += 1
      offAsk()
      offClosed()
      // These are backend-owned requests. Stop observing; backend session/timeout
      // policy remains responsible for resolving outstanding tool calls.
      queueRef.current = []
    }
  }, [])

  const decide = (id: string, decision: PermissionDecision): void => {
    if (!active.current || queueRef.current[0]?.id !== id || pendingDecisions.current.has(id))
      return
    const generation = lifetime.current
    pendingDecisions.current.add(id)
    void (async () => {
      try {
        await window.api.permission.decide(id, decision)
        if (generation !== lifetime.current) return
        queueRef.current = queueRef.current.filter((request) => request.id !== id)
        setQueue(queueRef.current)
      } catch (error) {
        if (generation === lifetime.current)
          notify(parseIpcThrownError(error).message || t('auth.connectionFailed'))
      } finally {
        pendingDecisions.current.delete(id)
      }
    })()
  }

  return { current: queue[0] ?? null, decide }
}

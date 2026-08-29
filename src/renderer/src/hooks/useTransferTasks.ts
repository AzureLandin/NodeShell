import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import type { TransferState, TransferTask } from '../../../shared/types'

export interface TransferMetrics {
  speed: number // bytes/sec
  eta: number | null // seconds remaining or null
}

export type TransferTaskWithMetrics = TransferTask & TransferMetrics

interface ProgressSample {
  timestamp: number
  transferred: number
}

const SPEED_WINDOW_MS = 2500
const SUCCEEDED_DISMISS_MS = 8000
const CANCELLED_DISMISS_MS = 12000
const FAILED_DISMISS_MS = 30000

const STATE_RANK: Record<TransferState, number> = {
  queued: 1,
  running: 2,
  finalizing: 3,
  succeeded: 4,
  failed: 4,
  cancelled: 4
}

const isTerminalState = (state: TransferState): boolean =>
  state === 'succeeded' || state === 'failed' || state === 'cancelled'

export function formatBytes(bytes: number): string {
  if (bytes <= 0 || !Number.isFinite(bytes)) return '0 B'
  const units = ['B', 'KB', 'MB', 'GB', 'TB']
  const i = Math.min(Math.floor(Math.log(bytes) / Math.log(1024)), units.length - 1)
  const val = bytes / Math.pow(1024, i)
  return `${val >= 10 || i === 0 ? val.toFixed(0) : val.toFixed(1)} ${units[i]}`
}

export function formatSpeed(bps: number): string {
  if (bps <= 0 || !Number.isFinite(bps)) return '0 B/s'
  return `${formatBytes(bps)}/s`
}

export function formatEta(seconds: number | null, isFinalizing = false): string {
  if (isFinalizing) return 'finalizing'
  if (seconds === null || !Number.isFinite(seconds) || seconds < 0) return 'calculating'
  if (seconds <= 1) return 'almostDone'
  if (seconds < 60) return `${Math.ceil(seconds)}s`
  const mins = Math.floor(seconds / 60)
  const secs = Math.ceil(seconds % 60)
  return `${mins}m ${secs}s`
}

export function useTransferTasks(isPaused = false): {
  tasks: TransferTaskWithMetrics[]
  activeCount: number
  cancellingIds: Set<string>
  cancel: (taskId: string) => Promise<void>
  retry: (taskId: string) => Promise<string | undefined>
  clear: (taskId: string) => Promise<void>
  clearCompleted: () => Promise<void>
} {
  const [tasksMap, setTasksMap] = useState<Map<string, TransferTask>>(new Map())
  const [cancellingIds, setCancellingIds] = useState<Set<string>>(new Set())

  // Samples for calculating speed and ETA
  const samplesRef = useRef<Map<string, ProgressSample[]>>(new Map())

  // Load initial tasks snapshot
  useEffect(() => {
    let unmounted = false
    void window.api.transfer.getTasks().then((list) => {
      if (unmounted) return
      setTasksMap(new Map(list.map((t) => [t.taskId, t])))
    })
    return () => {
      unmounted = true
    }
  }, [])

  // Subscribe to transfer events
  useEffect(() => {
    const unsub = window.api.transfer.onTask((incomingTask) => {
      setTasksMap((prev) => {
        let task = incomingTask
        const existing = prev.get(task.taskId)
        if (existing) {
          // If already in terminal state, ignore non-terminal events for this taskId
          if (isTerminalState(existing.state) && !isTerminalState(task.state)) {
            return prev
          }
          // Prevent state rank drop (e.g. from finalizing/running back to queued)
          if (STATE_RANK[task.state] < STATE_RANK[existing.state]) {
            return prev
          }
          // Preserve monotonic progress if incoming has lower transferred under same state
          if (task.state === existing.state && task.transferred < existing.transferred) {
            task = { ...task, transferred: existing.transferred }
          }
          // Preserve finishedAt if incoming terminal event missed it
          if (isTerminalState(task.state) && !task.finishedAt && existing.finishedAt) {
            task = { ...task, finishedAt: existing.finishedAt }
          }
        }
        const next = new Map(prev)
        next.set(task.taskId, task)
        return next
      })

      // Update samples for speed calculation
      if (incomingTask.state === 'running' || incomingTask.state === 'finalizing') {
        const now = Date.now()
        const currentSamples = samplesRef.current.get(incomingTask.taskId) ?? []
        const updated = [...currentSamples, { timestamp: now, transferred: incomingTask.transferred }].filter(
          (s) => now - s.timestamp <= SPEED_WINDOW_MS
        )
        samplesRef.current.set(incomingTask.taskId, updated)
      } else {
        samplesRef.current.delete(incomingTask.taskId)
        if (incomingTask.state === 'cancelled' || incomingTask.state === 'succeeded' || incomingTask.state === 'failed') {
          setCancellingIds((prev) => {
            if (!prev.has(incomingTask.taskId)) return prev
            const next = new Set(prev)
            next.delete(incomingTask.taskId)
            return next
          })
        }
      }
    })
    return unsub
  }, [])

  // Auto-dismiss finished tasks unless paused
  useEffect(() => {
    if (isPaused) return

    const interval = window.setInterval(() => {
      const now = Date.now()
      const toRemove: string[] = []

      for (const task of tasksMap.values()) {
        if (!task.finishedAt) continue
        const elapsed = now - task.finishedAt
        if (
          (task.state === 'succeeded' && elapsed >= SUCCEEDED_DISMISS_MS) ||
          (task.state === 'cancelled' && elapsed >= CANCELLED_DISMISS_MS) ||
          (task.state === 'failed' && elapsed >= FAILED_DISMISS_MS)
        ) {
          toRemove.push(task.taskId)
        }
      }

      if (toRemove.length > 0) {
        setTasksMap((prev) => {
          const next = new Map(prev)
          for (const id of toRemove) {
            next.delete(id)
            void window.api.transfer.clear(id).catch(() => undefined)
          }
          return next
        })
      }
    }, 1000)

    return () => window.clearInterval(interval)
  }, [isPaused, tasksMap])

  const cancel = useCallback(async (taskId: string) => {
    setCancellingIds((prev) => new Set(prev).add(taskId))
    try {
      await window.api.transfer.cancel(taskId)
    } catch {
      setCancellingIds((prev) => {
        const next = new Set(prev)
        next.delete(taskId)
        return next
      })
    }
  }, [])

  const retry = useCallback(async (taskId: string) => {
    try {
      const newId = await window.api.transfer.retry(taskId)
      return newId
    } catch {
      return undefined
    }
  }, [])

  const clear = useCallback(async (taskId: string) => {
    setTasksMap((prev) => {
      const next = new Map(prev)
      next.delete(taskId)
      return next
    })
    try {
      await window.api.transfer.clear(taskId)
    } catch {
      // ignore
    }
  }, [])

  const clearCompleted = useCallback(async () => {
    setTasksMap((prev) => {
      const next = new Map(prev)
      for (const [id, t] of prev.entries()) {
        if (t.state === 'succeeded' || t.state === 'failed' || t.state === 'cancelled') {
          next.delete(id)
        }
      }
      return next
    })
    try {
      await window.api.transfer.clearCompleted()
    } catch {
      // ignore
    }
  }, [])

  const tasksWithMetrics = useMemo(() => {
    const list = Array.from(tasksMap.values())

    return list
      .map((t) => {
        let speed = 0
        let eta: number | null = null

        if (t.state === 'running' || t.state === 'finalizing') {
          const samples = samplesRef.current.get(t.taskId) ?? []
          if (samples.length >= 2) {
            const first = samples[0]
            const last = samples[samples.length - 1]
            const dt = (last.timestamp - first.timestamp) / 1000
            const db = last.transferred - first.transferred
            if (dt > 0.2 && db >= 0) {
              speed = db / dt
            }
          }
          if (t.total > 0 && speed > 0) {
            const remaining = Math.max(0, t.total - t.transferred)
            eta = remaining / speed
          }
        }

        return {
          ...t,
          speed,
          eta
        }
      })
      .sort((a, b) => {
        // Active tasks first, then by createdAt desc
        const aActive = a.state === 'queued' || a.state === 'running' || a.state === 'finalizing'
        const bActive = b.state === 'queued' || b.state === 'running' || b.state === 'finalizing'
        if (aActive && !bActive) return -1
        if (!aActive && bActive) return 1
        return b.createdAt - a.createdAt
      })
  }, [tasksMap])

  const activeCount = useMemo(() => {
    let count = 0
    for (const t of tasksMap.values()) {
      if (t.state === 'queued' || t.state === 'running' || t.state === 'finalizing') {
        count++
      }
    }
    return count
  }, [tasksMap])

  return {
    tasks: tasksWithMetrics,
    activeCount,
    cancellingIds,
    cancel,
    retry,
    clear,
    clearCompleted
  }
}

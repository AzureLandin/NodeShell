import { useCallback, useEffect, useRef, useState, type Dispatch, type SetStateAction } from 'react'
import { parseIpcThrownError } from '../../../shared/ipc-error'
import type { ConnectOptions, HostConfig } from '../../../shared/types'

export type UiSessionStatus = 'connecting' | 'connected' | 'disconnected' | 'error'

export interface UiSession {
  sessionId: string
  hostId: string
  title: string
  status: UiSessionStatus
  errorMessage?: string
  authMethod: 'password' | 'privateKey'
  /** SSH endpoint for connecting / status UI (host name as configured). */
  remoteHost?: string
  remotePort?: number
}

export function isPendingSessionId(sessionId: string): boolean {
  return sessionId.startsWith('pending-')
}

export class ConnectError extends Error {
  constructor(
    public code: string,
    message: string
  ) {
    super(message)
    this.name = 'ConnectError'
  }
}

/** Bound background output while a tab's TerminalView is unmounted. */
const OUTPUT_RING_MAX = 96 * 1024

async function invokeConnect(
  hostId: string,
  options?: ConnectOptions
): Promise<{ sessionId: string }> {
  try {
    return await window.api.sessions.connect(hostId, options)
  } catch (e) {
    const { code, message } = parseIpcThrownError(e)
    if (code) throw new ConnectError(code, message)
    throw new Error(message)
  }
}

function appendRing(prev: string, data: string, max: number): string {
  const next = prev.length === 0 ? data : prev + data
  if (next.length <= max) return next
  return next.slice(next.length - max)
}

function removeSessionById(
  prev: UiSession[],
  sessionId: string,
  setActiveSessionId: Dispatch<SetStateAction<string | null>>
): UiSession[] {
  const next = prev.filter((s) => s.sessionId !== sessionId)
  setActiveSessionId((active) => {
    if (active !== sessionId) return active
    return next.length > 0 ? next[next.length - 1]!.sessionId : null
  })
  return next
}

export function useSessions(): {
  sessions: UiSession[]
  activeSessionId: string | null
  setActiveSessionId: Dispatch<SetStateAction<string | null>>
  toast: string | null
  setToast: Dispatch<SetStateAction<string | null>>
  connect: (host: HostConfig, options?: ConnectOptions) => Promise<void>
  disconnect: (sessionId: string) => Promise<void>
  abortConnectingUi: (hostId: string) => void
  reconnect: (session: UiSession, host: HostConfig, options?: ConnectOptions) => Promise<void>
  registerDataListener: (sessionId: string, cb: (data: string) => void) => () => void
} {
  const [sessions, setSessions] = useState<UiSession[]>([])
  const [activeSessionId, setActiveSessionId] = useState<string | null>(null)
  const [toast, setToast] = useState<string | null>(null)
  const dataListenersRef = useRef(new Map<string, (data: string) => void>())
  const outputRingsRef = useRef(new Map<string, string>())
  const sessionsRef = useRef<UiSession[]>([])
  sessionsRef.current = sessions

  useEffect(() => {
    const offData = window.api.sessions.onData(({ sessionId, data }) => {
      const listener = dataListenersRef.current.get(sessionId)
      if (listener) {
        listener(data)
        return
      }
      // Inactive / unmounted terminal — keep a bounded ring for replay on remount.
      const prev = outputRingsRef.current.get(sessionId) ?? ''
      outputRingsRef.current.set(sessionId, appendRing(prev, data, OUTPUT_RING_MAX))
    })
    const offClosed = window.api.sessions.onClosed(({ sessionId }) => {
      outputRingsRef.current.delete(sessionId)
      setSessions((prev) =>
        prev.map((s) => (s.sessionId === sessionId ? { ...s, status: 'disconnected' } : s))
      )
    })
    const offError = window.api.sessions.onError(({ sessionId, error }) => {
      setSessions((prev) =>
        prev.map((s) =>
          s.sessionId === sessionId ? { ...s, status: 'error', errorMessage: error.message } : s
        )
      )
      setToast(error.message)
    })
    return () => {
      offData()
      offClosed()
      offError()
    }
  }, [])

  const registerDataListener = useCallback(
    (sessionId: string, cb: (data: string) => void): (() => void) => {
      const buffered = outputRingsRef.current.get(sessionId)
      if (buffered) {
        outputRingsRef.current.delete(sessionId)
        cb(buffered)
      }
      dataListenersRef.current.set(sessionId, cb)
      return () => {
        dataListenersRef.current.delete(sessionId)
      }
    },
    []
  )

  const connect = useCallback(async (host: HostConfig, options?: ConnectOptions): Promise<void> => {
    // Resolve the pending id from the latest sessions snapshot — never from a
    // setState updater side effect (batched updates can run the updater later).
    const existing = sessionsRef.current.find(
      (s) => s.status === 'connecting' && s.hostId === host.id
    )
    const pendingId = existing?.sessionId ?? `pending-${crypto.randomUUID()}`
    if (!existing) {
      setSessions((prev) => {
        if (prev.some((s) => s.sessionId === pendingId || (s.status === 'connecting' && s.hostId === host.id))) {
          return prev
        }
        return [
          ...prev,
          {
            sessionId: pendingId,
            hostId: host.id,
            title: host.name,
            status: 'connecting' as const,
            authMethod: host.authMethod,
            remoteHost: host.host,
            remotePort: host.port
          }
        ]
      })
    }
    setActiveSessionId(pendingId)

    try {
      const { sessionId } = await invokeConnect(host.id, options)
      setSessions((prev) =>
        prev.map((s) =>
          s.sessionId === pendingId
            ? {
                sessionId,
                hostId: host.id,
                title: host.name,
                status: 'connected' as const,
                authMethod: host.authMethod,
                remoteHost: host.host,
                remotePort: host.port
              }
            : s
        )
      )
      setActiveSessionId(sessionId)
    } catch (e) {
      // Keep the pending tab during host-key confirm so the user stays on the
      // terminal view; App.abortConnectingUi cleans up if they decline.
      if (
        e instanceof ConnectError &&
        (e.code === 'HOST_KEY_CHANGED' || e.code === 'HOST_KEY_UNKNOWN')
      ) {
        throw e
      }
      setSessions((prev) => removeSessionById(prev, pendingId, setActiveSessionId))
      throw e
    }
  }, [])

  /** Remove a pending connect tab, or restore a reconnecting tab to disconnected. */
  const abortConnectingUi = useCallback((hostId: string): void => {
    setSessions((prev) => {
      const pending = prev.find(
        (s) =>
          s.status === 'connecting' && s.hostId === hostId && isPendingSessionId(s.sessionId)
      )
      if (pending) {
        return removeSessionById(prev, pending.sessionId, setActiveSessionId)
      }
      return prev.map((s) =>
        s.status === 'connecting' && s.hostId === hostId
          ? { ...s, status: 'disconnected' as const, errorMessage: undefined }
          : s
      )
    })
  }, [])

  const disconnect = useCallback(async (sessionId: string): Promise<void> => {
    if (isPendingSessionId(sessionId)) {
      try {
        await window.api.sessions.cancelConnect()
      } catch {
        /* ignore */
      }
    } else {
      try {
        await window.api.sessions.disconnect(sessionId)
      } catch {
        /* session may already be gone */
      }
    }
    outputRingsRef.current.delete(sessionId)
    setSessions((prev) => removeSessionById(prev, sessionId, setActiveSessionId))
    dataListenersRef.current.delete(sessionId)
  }, [])

  const reconnect = useCallback(
    async (session: UiSession, host: HostConfig, options?: ConnectOptions): Promise<void> => {
      // Connect first, then swap — keeps the old session alive if the new connect fails.
      setSessions((prev) =>
        prev.map((s) =>
          s.sessionId === session.sessionId ? { ...s, status: 'connecting' as const } : s
        )
      )
      try {
        const { sessionId } = await invokeConnect(host.id, options)
        const oldId = session.sessionId
        outputRingsRef.current.delete(oldId)
        setSessions((prev) =>
          prev.map((s) =>
            s.sessionId === oldId
              ? {
                  sessionId,
                  hostId: host.id,
                  title: host.name,
                  status: 'connected' as const,
                  authMethod: host.authMethod,
                  remoteHost: host.host,
                  remotePort: host.port
                }
              : s
          )
        )
        setActiveSessionId(sessionId)
        try {
          await window.api.sessions.disconnect(oldId)
        } catch {
          /* old session may already be gone */
        }
        dataListenersRef.current.delete(oldId)
      } catch (e) {
        const { code, message } =
          e instanceof ConnectError
            ? { code: e.code, message: e.message }
            : parseIpcThrownError(e)
        if (code === 'HOST_KEY_CHANGED' || code === 'HOST_KEY_UNKNOWN') {
          // Leave status as connecting for the confirm dialog; App restores on decline.
          if (code) throw new ConnectError(code, message)
        }
        if (code) {
          setSessions((prev) =>
            prev.map((s) =>
              s.sessionId === session.sessionId
                ? { ...s, status: 'disconnected' as const, errorMessage: undefined }
                : s
            )
          )
          throw new ConnectError(code, message)
        }
        setToast(message)
        setSessions((prev) =>
          prev.map((s) =>
            s.sessionId === session.sessionId
              ? { ...s, status: 'error', errorMessage: message }
              : s
          )
        )
      }
    },
    []
  )

  return {
    sessions,
    activeSessionId,
    setActiveSessionId,
    toast,
    setToast,
    connect,
    disconnect,
    abortConnectingUi,
    reconnect,
    registerDataListener
  }
}

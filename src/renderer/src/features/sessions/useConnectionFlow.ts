import { useLayoutEffect, useReducer, useRef } from 'react'
import { useTranslation } from 'react-i18next'
import type { ConnectOptions, HostConfig } from '../../../../shared/types'
import type { Confirmation } from '../../ui/confirmation'
import { useNotify } from '../../ui/use-notify'
import { hostKeyFingerprintLabel, localizeConnectError } from './connect-errors'
import { ConnectError, useSessions, type UiSession } from './useSessions'

type ConnectionAction =
  | { type: 'connect'; host: HostConfig }
  | { type: 'reconnect'; host: HostConfig; session: UiSession }

type FlowState =
  | { phase: 'idle' }
  | { phase: 'password'; action: ConnectionAction; error: string | null }
  | { phase: 'connecting' | 'confirming' | 'credentials'; action: ConnectionAction }

type FlowEvent =
  | { type: 'start'; action: ConnectionAction }
  | { type: 'password'; action: ConnectionAction; error: string | null }
  | { type: 'confirming' | 'credentials' }
  | { type: 'finish' | 'cancel-password' }

function reduceFlow(state: FlowState, event: FlowEvent): FlowState {
  switch (event.type) {
    case 'start':
      return { phase: 'connecting', action: event.action }
    case 'password':
      return { phase: 'password', action: event.action, error: event.error }
    case 'confirming':
    case 'credentials':
      return state.phase === 'idle' || state.phase === 'password'
        ? state
        : { phase: event.type, action: state.action }
    case 'finish':
      return state.phase === 'password' ? state : { phase: 'idle' }
    case 'cancel-password':
      return state.phase === 'password' ? { phase: 'idle' } : state
  }
}

interface FlowDependencies {
  hosts: HostConfig[]
  refreshHosts: () => Promise<HostConfig[] | undefined>
  askConfirm: (request: Confirmation) => Promise<boolean>
  onConnectStart: () => void
}

export function useConnectionFlow({
  hosts,
  refreshHosts,
  askConfirm,
  onConnectStart
}: FlowDependencies): {
  session: ReturnType<typeof useSessions>
  state: FlowState
  connecting: boolean
  connectingHost: HostConfig | null
  connect: (host: HostConfig, options?: ConnectOptions) => void
  reconnect: (session: UiSession) => void
  submitPassword: (password: string) => void
  dismissPassword: () => void
  cancelConnect: () => void
  reportCredentialError: (error: unknown) => void
} {
  const { t } = useTranslation()
  const notify = useNotify()
  const session = useSessions((message) =>
    notify(localizeConnectError(t, message, 'auth.connectionFailed'))
  )
  const [state, dispatch] = useReducer(reduceFlow, { phase: 'idle' })
  const busy = useRef(false)
  const lifetime = useRef(0)
  const mounted = useRef(false)
  const prompted = useRef(new Set<string>())

  useLayoutEffect(() => {
    mounted.current = true
    lifetime.current += 1
    return () => {
      mounted.current = false
      lifetime.current += 1
    }
  }, [])

  const reportCredentialError = (error: unknown): void => {
    if (mounted.current) notify(localizeConnectError(t, error, 'auth.credentialsUnavailable'))
  }

  const maybeSaveCredentials = async (
    host: HostConfig,
    password: string | undefined,
    generation: number
  ): Promise<void> => {
    const latest = (await refreshHosts())?.find((item) => item.id === host.id)
    if (
      generation !== lifetime.current ||
      !latest ||
      latest.credentialsPrompted ||
      prompted.current.has(latest.id)
    )
      return
    prompted.current.add(latest.id)
    const save = await askConfirm({
      title: t('auth.saveCredentialsTitle'),
      message: t('auth.saveCredentials', { name: latest.name }),
      confirmLabel: t('auth.saveCredentialsConfirm'),
      cancelLabel: t('auth.saveCredentialsSkip')
    })
    if (generation !== lifetime.current) return
    try {
      if (!save) {
        await window.api.credentials.markPrompted(latest.id, false)
      } else {
        const available = await window.api.credentials.isAvailable()
        if (generation !== lifetime.current) return
        if (!available) {
          notify(t('auth.credentialsUnavailable'))
          await window.api.credentials.markPrompted(latest.id, false)
        } else {
          await window.api.credentials.save(latest.id, {
            ...(password ? { password } : {}),
            ...(latest.privateKeyPath ? { privateKeyPath: latest.privateKeyPath } : {})
          })
        }
      }
      if (generation === lifetime.current) await refreshHosts()
    } catch (error) {
      prompted.current.delete(latest.id)
      if (generation === lifetime.current) reportCredentialError(error)
    }
  }

  const attempt = async (action: ConnectionAction, options?: ConnectOptions): Promise<void> => {
    if (busy.current || !mounted.current) return
    busy.current = true
    const generation = lifetime.current
    dispatch({ type: 'start', action })
    if (action.type === 'connect') onConnectStart()
    const { host } = action
    let nextOptions = options
    try {
      while (generation === lifetime.current) {
        try {
          if (action.type === 'connect') await session.connect(host, nextOptions)
          else await session.reconnect(action.session, host, nextOptions)
        } catch (error) {
          if (generation !== lifetime.current) return
          if (
            error instanceof ConnectError &&
            (error.code === 'HOST_KEY_UNKNOWN' || error.code === 'HOST_KEY_CHANGED')
          ) {
            dispatch({ type: 'confirming' })
            const unknown = error.code === 'HOST_KEY_UNKNOWN'
            const accept = await askConfirm({
              title: t(unknown ? 'auth.hostKeyUnknownTitle' : 'auth.hostKeyChangedTitle'),
              message: t(
                action.type === 'connect'
                  ? unknown
                    ? 'auth.hostKeyUnknown'
                    : 'auth.hostKeyChanged'
                  : unknown
                    ? 'auth.hostKeyUnknownReconnect'
                    : 'auth.hostKeyChangedReconnect',
                { fingerprint: hostKeyFingerprintLabel(error.message) }
              )
            })
            if (generation !== lifetime.current) return
            if (accept) {
              nextOptions = { ...nextOptions, acceptHostKey: true }
              dispatch({ type: 'start', action })
              continue
            }
            session.abortConnectingUi(host.id)
          } else if (
            error instanceof ConnectError &&
            error.code === 'AUTH_FAILED' &&
            host.authMethod === 'password'
          ) {
            dispatch({ type: 'password', action, error: t('auth.authFailed') })
            return
          }
          notify(
            localizeConnectError(
              t,
              error,
              action.type === 'connect' ? 'auth.connectionFailed' : 'auth.reconnectFailed',
              host
            )
          )
          return
        }
        if (generation !== lifetime.current) return
        if (action.type === 'connect') {
          dispatch({ type: 'credentials' })
          await maybeSaveCredentials(host, options?.password, generation)
        }
        return
      }
    } finally {
      if (generation === lifetime.current) {
        busy.current = false
        dispatch({ type: 'finish' })
      }
    }
  }

  const connect = (host: HostConfig, options?: ConnectOptions): void => {
    if (busy.current || !mounted.current) return
    if (!options && host.authMethod === 'password' && !host.credentialsSaved) {
      dispatch({ type: 'password', action: { type: 'connect', host }, error: null })
    } else void attempt({ type: 'connect', host }, options)
  }

  const reconnect = (target: UiSession): void => {
    if (busy.current || !mounted.current) return
    const host = hosts.find((item) => item.id === target.hostId)
    if (!host) {
      notify(t('auth.hostNotFound'))
      return
    }
    const action: ConnectionAction = { type: 'reconnect', host, session: target }
    if (host.authMethod === 'password' && !host.credentialsSaved) {
      dispatch({ type: 'password', action, error: null })
    } else void attempt(action)
  }

  const submitPassword = (password: string): void => {
    if (state.phase === 'password') void attempt(state.action, { password })
  }

  const cancelConnect = (): void => {
    void (async () => {
      try {
        await window.api.sessions.cancelConnect()
      } catch (error) {
        if (mounted.current) notify(localizeConnectError(t, error, 'auth.connectionFailed'))
      }
    })()
  }

  return {
    session,
    state,
    connecting: state.phase !== 'idle' && state.phase !== 'password',
    connectingHost: state.phase === 'idle' ? null : state.action.host,
    connect,
    reconnect,
    submitPassword,
    dismissPassword: () => {
      if (!busy.current) dispatch({ type: 'cancel-password' })
    },
    cancelConnect,
    reportCredentialError
  }
}

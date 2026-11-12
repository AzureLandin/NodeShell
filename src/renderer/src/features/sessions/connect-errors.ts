import type { TFunction } from 'i18next'
import type { HostConfig } from '../../../../shared/types'
import { parseIpcThrownError } from '../../../../shared/ipc-error'
import { ConnectError } from './useSessions'

export function localizeConnectError(
  t: TFunction,
  e: unknown,
  fallbackKey: 'auth.connectionFailed' | 'auth.reconnectFailed' | 'auth.credentialsUnavailable',
  host?: HostConfig | null
): string {
  const parsed =
    e instanceof ConnectError ? { code: e.code, message: e.message } : parseIpcThrownError(e)
  const code = parsed.code
  const parsedMessage = parsed.message
  const addr = {
    name: host?.name ?? '',
    host: host?.host ?? '',
    port: host?.port ?? ''
  }
  if (code === 'CANCELLED') return t('auth.cancelled')
  if (
    code === 'AUTH_FAILED' ||
    /authentication failed/i.test(parsedMessage) ||
    parsedMessage.includes('"AUTH_FAILED"')
  ) {
    return t('auth.authFailed')
  }
  if (code === 'CONNECTION_REFUSED') return t('auth.connectionRefused', addr)
  if (code === 'HOST_UNREACHABLE') return t('auth.hostUnreachable', addr)
  if (code === 'TIMEOUT') return t('auth.timeout', addr)
  return parsedMessage || t(fallbackKey)
}

export function hostKeyFingerprintLabel(message: string): string {
  // Go's HOST_KEY_CHANGED error carries both keys
  // ("was SHA256:<old>, now SHA256:<new>"); the dialog must show the NEW
  // key being accepted, so take the last SHA256 match (HOST_KEY_UNKNOWN has
  // a single match, so last == first and is unaffected).
  const matches = message.match(/SHA256:[A-Za-z0-9+/=]+/g)
  const last = matches?.[matches.length - 1]
  return last ? `SHA256:\n${last.slice('SHA256:'.length)}` : message
}

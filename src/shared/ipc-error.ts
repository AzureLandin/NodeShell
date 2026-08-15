import type { AppError, SshErrorCode } from './types'

const SSH_CODES: ReadonlySet<string> = new Set([
  'CONNECTION_REFUSED',
  'TIMEOUT',
  'AUTH_FAILED',
  'HOST_UNREACHABLE',
  'HOST_KEY_CHANGED',
  'HOST_KEY_UNKNOWN',
  'CONFIG_READ_FAILED',
  'CONFIG_WRITE_FAILED',
  'SESSION_NOT_FOUND',
  'MCP_SESSION_LIMIT',
  'HOST_NOT_FOUND',
  'CANCELLED',
  'PERMISSION_DENIED',
  'UNKNOWN'
])

const IPC_ERR_PREFIX = 'NODESHELL_ERR:'

export function isAppError(err: unknown): err is AppError {
  if (!err || typeof err !== 'object') return false
  const o = err as Record<string, unknown>
  return typeof o.code === 'string' && typeof o.message === 'string' && SSH_CODES.has(o.code)
}

/**
 * Electron may stringify thrown Errors as `Name: message` across IPC.
 * Use a stable, parseable payload instead of JSON-as-Error-name.
 */
export function toIpcThrownError(err: unknown): Error {
  const app = normalizeToAppError(err)
  return new Error(`${IPC_ERR_PREFIX}${app.code}:${app.message}`)
}

function normalizeToAppError(err: unknown): AppError {
  if (isAppError(err)) return err
  if (err instanceof Error) {
    return { code: 'UNKNOWN', message: err.message || 'Unknown error' }
  }
  if (err && typeof err === 'object' && 'message' in err) {
    const message = String((err as { message: unknown }).message)
    const code =
      'code' in err && typeof (err as { code: unknown }).code === 'string'
        ? (err as { code: string }).code
        : 'UNKNOWN'
    return {
      code: SSH_CODES.has(code) ? (code as SshErrorCode) : 'UNKNOWN',
      message
    }
  }
  return { code: 'UNKNOWN', message: String(err) }
}

function tryParseJsonAppError(text: string): AppError | null {
  const trimmed = text.trim()
  if (!trimmed.startsWith('{')) return null
  try {
    const parsed = JSON.parse(trimmed) as { code?: unknown; message?: unknown }
    if (typeof parsed.message === 'string') {
      const code =
        typeof parsed.code === 'string' && SSH_CODES.has(parsed.code)
          ? (parsed.code as SshErrorCode)
          : 'UNKNOWN'
      return { code, message: parsed.message }
    }
  } catch {
    /* ignore */
  }
  return null
}

function tryParsePrefixed(text: string): AppError | null {
  const idx = text.indexOf(IPC_ERR_PREFIX)
  if (idx < 0) return null
  const body = text.slice(idx + IPC_ERR_PREFIX.length)
  const colon = body.indexOf(':')
  if (colon <= 0) return null
  const code = body.slice(0, colon)
  const message = body.slice(colon + 1)
  if (!message) return null
  return {
    code: SSH_CODES.has(code) ? (code as SshErrorCode) : 'UNKNOWN',
    message
  }
}

export function parseIpcThrownError(e: unknown): { code?: SshErrorCode; message: string } {
  const raw = e instanceof Error ? e.message : typeof e === 'string' ? e : String(e)

  const invokeMatch = raw.match(/^Error invoking remote method '[^']+': ([\s\S]+)$/)
  let payload = (invokeMatch?.[1] ?? raw).trim()

  // Electron may turn thrown Error into `AppError: {...}` or `Error: NODESHELL_ERR:...`
  const namePrefix = payload.match(/^(AppError|Error):\s*([\s\S]+)$/)
  if (namePrefix) payload = namePrefix[2]!.trim()

  if (payload === '[object Object]') {
    return { code: 'UNKNOWN', message: 'Connection failed' }
  }

  const fromPrefix = tryParsePrefixed(payload) ?? tryParsePrefixed(raw)
  if (fromPrefix) return fromPrefix

  const fromJson = tryParseJsonAppError(payload)
  if (fromJson) return fromJson

  return { message: payload || 'Connection failed' }
}

export async function withIpcErrors<T>(fn: () => Promise<T>): Promise<T> {
  try {
    return await fn()
  } catch (err) {
    throw toIpcThrownError(err)
  }
}

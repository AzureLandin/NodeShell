import { describe, expect, it } from 'vitest'
import { parseIpcThrownError, toIpcThrownError } from '../src/shared/ipc-error'

describe('ipc-error', () => {
  it('serializes AppError with a stable prefix', () => {
    const e = toIpcThrownError({ code: 'AUTH_FAILED', message: 'Authentication failed' })
    expect(e.message).toBe('NODESHELL_ERR:AUTH_FAILED:Authentication failed')
  })

  it('parses Electron invoke wrapper + prefixed payload', () => {
    const wrapped = new Error(
      "Error invoking remote method 'sessions:connect': NODESHELL_ERR:TIMEOUT:Connection timed out"
    )
    expect(parseIpcThrownError(wrapped)).toEqual({
      code: 'TIMEOUT',
      message: 'Connection timed out'
    })
  })

  it('parses legacy AppError JSON toString form', () => {
    expect(
      parseIpcThrownError(
        new Error(
          'AppError: {"code":"AUTH_FAILED","message":"Authentication failed"}'
        )
      )
    ).toEqual({
      code: 'AUTH_FAILED',
      message: 'Authentication failed'
    })
  })

  it('parses invoke + AppError JSON form', () => {
    expect(
      parseIpcThrownError(
        new Error(
          `Error invoking remote method 'sessions:connect': AppError: ${JSON.stringify({
            code: 'AUTH_FAILED',
            message: 'Authentication failed'
          })}`
        )
      )
    ).toEqual({
      code: 'AUTH_FAILED',
      message: 'Authentication failed'
    })
  })

  it('maps legacy [object Object] invoke errors to a readable fallback', () => {
    expect(
      parseIpcThrownError(
        new Error("Error invoking remote method 'sessions:connect': [object Object]")
      )
    ).toEqual({ code: 'UNKNOWN', message: 'Connection failed' })
  })

  it('round-trips CANCELLED across IPC', () => {
    const e = toIpcThrownError({ code: 'CANCELLED', message: 'Connection cancelled' })
    expect(parseIpcThrownError(e)).toEqual({
      code: 'CANCELLED',
      message: 'Connection cancelled'
    })
  })

  it('round-trips HOST_NOT_FOUND across IPC (Go emits it for DNS failures)', () => {
    const e = toIpcThrownError({ code: 'HOST_NOT_FOUND', message: 'Host not found: nope.invalid' })
    expect(parseIpcThrownError(e)).toEqual({
      code: 'HOST_NOT_FOUND',
      message: 'Host not found: nope.invalid'
    })
  })

  it('parses a Go-formatted NODESHELL_ERR:HOST_NOT_FOUND payload', () => {
    expect(
      parseIpcThrownError(new Error('NODESHELL_ERR:HOST_NOT_FOUND:Host not found: x'))
    ).toEqual({ code: 'HOST_NOT_FOUND', message: 'Host not found: x' })
  })
})

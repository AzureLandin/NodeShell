// @vitest-environment jsdom
import type { ReactNode } from 'react'
import { I18nextProvider } from 'react-i18next'
import { act, renderHook, screen } from '@testing-library/react'
import { describe, expect, it, vi } from 'vitest'
import { NotificationProvider } from '../../src/renderer/src/ui/notifications'
import { usePermissionQueue } from '../../src/renderer/src/features/permissions'
import type { PermissionAskEvent } from '../../src/shared/types'
import {
  createTestI18n,
  emitPermissionAsk,
  emitPermissionClosed,
  installFakeApi
} from '../support/helpers'

function wrapper({ children }: { children: ReactNode }): React.JSX.Element {
  return (
    <I18nextProvider i18n={createTestI18n()}>
      <NotificationProvider>{children}</NotificationProvider>
    </I18nextProvider>
  )
}

function request(id: string, source: PermissionAskEvent['source'] = 'mcp'): PermissionAskEvent {
  return { id, source, tool: 'bash', sessionId: 's1', title: 'Host', summary: 'uptime' }
}

describe('backend permission request ownership', () => {
  it('deduplicates requests, preserves FIFO, and rejects actions for closed requests', async () => {
    const fake = installFakeApi()
    const openAgent = vi.fn()
    const { result } = renderHook(() => usePermissionQueue(openAgent), { wrapper })
    act(() => {
      emitPermissionAsk(fake, request('one', 'agent'))
      emitPermissionAsk(fake, request('one', 'agent'))
      emitPermissionAsk(fake, request('two'))
    })
    expect(openAgent).toHaveBeenCalledTimes(1)
    expect(result.current.current?.id).toBe('one')
    const decide = result.current.decide
    act(() => emitPermissionClosed(fake, { id: 'one' }))
    expect(result.current.current?.id).toBe('two')
    act(() => decide('one', 'allow'))
    expect(fake.mocks.permission.decide).not.toHaveBeenCalled()
    await act(async () => result.current.decide('two', 'deny'))
    expect(fake.mocks.permission.decide).toHaveBeenCalledExactlyOnceWith('two', 'deny')
    expect(result.current.current).toBeNull()
  })

  it('prevents duplicate decisions and preserves a rejected request for retry', async () => {
    const fake = installFakeApi()
    let reject!: (error: Error) => void
    fake.mocks.permission.decide.mockReturnValueOnce(
      new Promise<void>((_resolve, no) => {
        reject = no
      })
    )
    const { result } = renderHook(() => usePermissionQueue(vi.fn()), { wrapper })
    act(() => {
      emitPermissionAsk(fake, request('one'))
      emitPermissionAsk(fake, request('two'))
    })
    act(() => {
      result.current.decide('one', 'allow')
      result.current.decide('one', 'deny')
    })
    expect(fake.mocks.permission.decide).toHaveBeenCalledTimes(1)
    expect(result.current.current?.id).toBe('one')
    await act(async () => reject(new Error('NODESHELL_ERR:UNKNOWN:decision denied')))
    expect(screen.getByRole('status')).toHaveTextContent('decision denied')
    expect(result.current.current?.id).toBe('one')
    await act(async () => result.current.decide('one', 'deny'))
    expect(fake.mocks.permission.decide).toHaveBeenCalledTimes(2)
    expect(result.current.current?.id).toBe('two')
  })

  it('releases subscriptions and ignores a late decision failure after disposal', async () => {
    const fake = installFakeApi()
    const offAsk = vi.fn()
    const offClosed = vi.fn()
    fake.mocks.permission.onAsk.mockReturnValue(offAsk)
    fake.mocks.permission.onClosed.mockReturnValue(offClosed)
    let reject!: (error: Error) => void
    fake.mocks.permission.decide.mockReturnValueOnce(
      new Promise<void>((_resolve, no) => {
        reject = no
      })
    )
    const view = renderHook(() => usePermissionQueue(vi.fn()), { wrapper })
    act(() => emitPermissionAsk(fake, request('one')))
    const decide = view.result.current.decide
    act(() => decide('one', 'allow'))
    view.unmount()
    expect(offAsk).toHaveBeenCalledTimes(1)
    expect(offClosed).toHaveBeenCalledTimes(1)
    await act(async () => reject(new Error('disposed request')))
    decide('one', 'allow')
    expect(fake.mocks.permission.decide).toHaveBeenCalledTimes(1)
    expect(screen.queryByRole('status')).not.toBeInTheDocument()
  })
})

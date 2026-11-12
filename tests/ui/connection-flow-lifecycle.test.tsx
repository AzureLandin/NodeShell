// @vitest-environment jsdom
import type { ReactNode } from 'react'
import { I18nextProvider } from 'react-i18next'
import { act, renderHook, waitFor } from '@testing-library/react'
import { describe, expect, it, vi } from 'vitest'
import { useConnectionFlow } from '../../src/renderer/src/features/sessions'
import { useConfirmationQueue } from '../../src/renderer/src/app/use-confirmation-queue'
import { NotificationProvider } from '../../src/renderer/src/ui/notifications'
import type { HostConfig } from '../../src/shared/types'
import { createTestI18n, installFakeApi } from '../support/helpers'

const host: HostConfig = {
  id: 'h1',
  name: 'Server',
  host: 'example.com',
  port: 22,
  username: 'azure',
  authMethod: 'privateKey',
  credentialsPrompted: false
}

function wrapper({ children }: { children: ReactNode }): React.JSX.Element {
  return (
    <I18nextProvider i18n={createTestI18n()}>
      <NotificationProvider>{children}</NotificationProvider>
    </I18nextProvider>
  )
}

describe('connection coordinator lifetime', () => {
  it('does not retry a host-key confirmation cancelled by application disposal', async () => {
    const fake = installFakeApi()
    fake.mocks.sessions.connect.mockRejectedValueOnce(
      new Error('NODESHELL_ERR:HOST_KEY_UNKNOWN:Unknown SHA256:example')
    )
    const refreshHosts = vi.fn(async () => [host])
    const view = renderHook(
      () => {
        const confirmation = useConfirmationQueue()
        const flow = useConnectionFlow({
          hosts: [host],
          refreshHosts,
          askConfirm: confirmation.ask,
          onConnectStart: vi.fn()
        })
        return { flow, confirmation }
      },
      { wrapper }
    )
    act(() => view.result.current.flow.connect(host))
    await waitFor(() => expect(view.result.current.confirmation.current).not.toBeNull())
    expect(view.result.current.flow.state.phase).toBe('confirming')
    view.unmount()
    await act(async () => undefined)
    expect(fake.mocks.sessions.connect).toHaveBeenCalledTimes(1)
    expect(refreshHosts).not.toHaveBeenCalled()
    expect(fake.mocks.credentials.markPrompted).not.toHaveBeenCalled()
  })

  it('does not start credential work when a connect returns after disposal', async () => {
    const fake = installFakeApi()
    let resolve!: (value: { sessionId: string }) => void
    fake.mocks.sessions.connect.mockReturnValueOnce(
      new Promise((yes) => {
        resolve = yes
      })
    )
    const refreshHosts = vi.fn(async () => [host])
    const askConfirm = vi.fn(async () => true)
    const view = renderHook(
      () => useConnectionFlow({ hosts: [host], refreshHosts, askConfirm, onConnectStart: vi.fn() }),
      { wrapper }
    )
    act(() => view.result.current.connect(host))
    const connect = view.result.current.connect
    view.unmount()
    await act(async () => resolve({ sessionId: 'late-session' }))
    connect(host)
    expect(fake.mocks.sessions.connect).toHaveBeenCalledTimes(1)
    expect(refreshHosts).not.toHaveBeenCalled()
    expect(askConfirm).not.toHaveBeenCalled()
    expect(fake.mocks.credentials.save).not.toHaveBeenCalled()
  })

  it('accepts only one command while connecting and releases the coordinator after failure', async () => {
    const fake = installFakeApi()
    let reject!: (error: Error) => void
    fake.mocks.sessions.connect
      .mockReturnValueOnce(
        new Promise((_resolve, no) => {
          reject = no
        })
      )
      .mockRejectedValueOnce(new Error('NODESHELL_ERR:CANCELLED:Cancelled'))
    const onConnectStart = vi.fn()
    const { result } = renderHook(
      () =>
        useConnectionFlow({
          hosts: [host],
          refreshHosts: vi.fn(async () => [host]),
          askConfirm: vi.fn(async () => false),
          onConnectStart
        }),
      { wrapper }
    )
    act(() => {
      result.current.connect(host)
      result.current.connect(host)
    })
    expect(fake.mocks.sessions.connect).toHaveBeenCalledTimes(1)
    await act(async () => reject(new Error('NODESHELL_ERR:CANCELLED:Cancelled')))
    expect(result.current.state.phase).toBe('idle')
    await act(async () => result.current.connect(host))
    expect(fake.mocks.sessions.connect).toHaveBeenCalledTimes(2)
    expect(onConnectStart).toHaveBeenCalledTimes(2)
  })
})

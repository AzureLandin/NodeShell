// @vitest-environment jsdom
import type { ReactNode } from 'react'
import { I18nextProvider } from 'react-i18next'
import { act, renderHook, screen, waitFor } from '@testing-library/react'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { NotificationProvider } from '../../src/renderer/src/ui/notifications'
import { useAppSettings } from '../../src/renderer/src/features/settings'
import type { AppSettings } from '../../src/shared/types'
import { createTestI18n, installFakeApi } from '../support/helpers'

const saved: AppSettings = {
  language: 'en',
  themePreference: 'light',
  terminalFontFamily: 'Hack',
  terminalFontSize: 14,
  mcpIdleTimeoutMinutes: 10,
  mcpMaxSessions: 8,
  mcpPermissionMode: 'external',
  permissionPolicy: 'ask'
}

function deferred<T>(): {
  promise: Promise<T>
  resolve: (value: T) => void
  reject: (error: Error) => void
} {
  let resolve!: (value: T) => void
  let reject!: (error: Error) => void
  const promise = new Promise<T>((yes, no) => {
    resolve = yes
    reject = no
  })
  return { promise, resolve, reject }
}

function wrapper({ children }: { children: ReactNode }): React.JSX.Element {
  return (
    <I18nextProvider i18n={createTestI18n()}>
      <NotificationProvider>{children}</NotificationProvider>
    </I18nextProvider>
  )
}

afterEach(() => vi.useRealTimers())

describe('application settings ownership', () => {
  it('applies preferences and rolls a failed optimistic save back to confirmed data', async () => {
    const fake = installFakeApi()
    fake.mocks.settings.get.mockResolvedValue(saved)
    const failure = deferred<AppSettings>()
    fake.mocks.settings.set.mockReturnValue(failure.promise)
    const { result } = renderHook(() => useAppSettings(), { wrapper })
    await waitFor(() => expect(result.current.values.language).toBe('en'))
    expect(document.documentElement.dataset.theme).toBe('light')
    act(() => result.current.change('themePreference', 'dark'))
    expect(result.current.resolvedTheme).toBe('dark')
    await act(async () => failure.reject(new Error('write denied')))
    expect(result.current.values.themePreference).toBe('light')
    expect(document.documentElement.dataset.theme).toBe('light')
    expect(screen.getByRole('status')).toHaveTextContent('Connection failed')
  })

  it('serializes changes to a preference without letting an earlier save replace the latest UI', async () => {
    const fake = installFakeApi()
    fake.mocks.settings.get.mockResolvedValue(saved)
    const first = deferred<AppSettings>()
    const second = deferred<AppSettings>()
    fake.mocks.settings.set.mockReturnValueOnce(first.promise).mockReturnValueOnce(second.promise)
    const { result } = renderHook(() => useAppSettings(), { wrapper })
    await waitFor(() => expect(result.current.values.language).toBe('en'))
    act(() => {
      result.current.change('terminalFontFamily', 'Menlo')
      result.current.change('terminalFontFamily', 'Monaco')
    })
    await waitFor(() => expect(fake.mocks.settings.set).toHaveBeenCalledTimes(1))
    expect(result.current.values.terminalFontFamily).toBe('Monaco')
    await act(async () => first.resolve({ ...saved, terminalFontFamily: 'Menlo' }))
    expect(fake.mocks.settings.set).toHaveBeenNthCalledWith(2, { terminalFontFamily: 'Monaco' })
    expect(result.current.values.terminalFontFamily).toBe('Monaco')
    await act(async () => second.reject(new Error('latest write denied')))
    expect(result.current.values.terminalFontFamily).toBe('Menlo')
  })

  it('ignores an earlier failure while a newer value is queued', async () => {
    const fake = installFakeApi()
    fake.mocks.settings.get.mockResolvedValue(saved)
    const first = deferred<AppSettings>()
    fake.mocks.settings.set
      .mockReturnValueOnce(first.promise)
      .mockResolvedValueOnce({ ...saved, mcpMaxSessions: 12 })
    const { result } = renderHook(() => useAppSettings(), { wrapper })
    await waitFor(() => expect(result.current.values.language).toBe('en'))
    act(() => {
      result.current.change('mcpMaxSessions', 10)
      result.current.change('mcpMaxSessions', 12)
    })
    await act(async () => first.reject(new Error('earlier failure')))
    await waitFor(() => expect(fake.mocks.settings.set).toHaveBeenCalledTimes(2))
    expect(result.current.values.mcpMaxSessions).toBe(12)
    expect(screen.queryByRole('status')).not.toBeInTheDocument()
  })

  it('does not let a late initial read override an edited or confirmed preference', async () => {
    const fake = installFakeApi()
    const read = deferred<AppSettings>()
    fake.mocks.settings.get.mockReturnValue(read.promise)
    fake.mocks.settings.set
      .mockResolvedValueOnce({ ...saved, terminalFontFamily: 'Menlo' })
      .mockRejectedValueOnce(new Error('write denied'))
    const { result } = renderHook(() => useAppSettings(), { wrapper })
    await act(async () => result.current.change('terminalFontFamily', 'Menlo'))
    await act(async () => read.resolve(saved))
    expect(result.current.values.terminalFontFamily).toBe('Menlo')
    await act(async () => result.current.change('terminalFontFamily', 'Monaco'))
    expect(result.current.values.terminalFontFamily).toBe('Menlo')
  })

  it('debounces font changes and cancels an unsent change on unmount', async () => {
    vi.useFakeTimers()
    const fake = installFakeApi()
    fake.mocks.settings.get.mockResolvedValue(saved)
    fake.mocks.settings.set.mockImplementation(async (patch) => ({ ...saved, ...patch }))
    const view = renderHook(() => useAppSettings(), { wrapper })
    await act(async () => undefined)
    act(() => {
      view.result.current.change('terminalFontSize', 16)
      view.result.current.change('terminalFontSize', 18)
    })
    await act(async () => {
      await vi.advanceTimersByTimeAsync(299)
    })
    expect(fake.mocks.settings.set).not.toHaveBeenCalled()
    await act(async () => {
      await vi.advanceTimersByTimeAsync(1)
    })
    expect(fake.mocks.settings.set).toHaveBeenCalledExactlyOnceWith({ terminalFontSize: 18 })
    act(() => view.result.current.change('terminalFontSize', 20))
    const change = view.result.current.change
    view.unmount()
    change('themePreference', 'dark')
    await act(async () => {
      await vi.advanceTimersByTimeAsync(300)
    })
    expect(fake.mocks.settings.set).toHaveBeenCalledTimes(1)
  })

  it('ignores disposed reads and writes across StrictMode replay and a fresh application', async () => {
    const fake = installFakeApi()
    const staleRead = deferred<AppSettings>()
    fake.mocks.settings.get.mockReturnValueOnce(staleRead.promise).mockResolvedValueOnce(saved)
    const view = renderHook(() => useAppSettings(), { wrapper, reactStrictMode: true })
    await waitFor(() => expect(view.result.current.values.language).toBe('en'))
    expect(fake.mocks.settings.get).toHaveBeenCalledTimes(2)
    await act(async () => staleRead.resolve({ ...saved, themePreference: 'dark' }))
    expect(view.result.current.values.themePreference).toBe('light')
    const write = deferred<AppSettings>()
    fake.mocks.settings.set.mockReturnValue(write.promise)
    await act(async () => view.result.current.change('themePreference', 'dark'))
    view.unmount()
    fake.mocks.settings.get.mockResolvedValue(saved)
    const next = renderHook(() => useAppSettings(), { wrapper })
    await waitFor(() => expect(next.result.current.values.language).toBe('en'))
    await act(async () => write.reject(new Error('disposed failure')))
    expect(next.result.current.values.themePreference).toBe('light')
    expect(screen.queryByRole('status')).not.toBeInTheDocument()
  })
})

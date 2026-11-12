// @vitest-environment jsdom
import { StrictMode, type ReactNode } from 'react'
import { I18nextProvider } from 'react-i18next'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { act, fireEvent, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { NotificationProvider } from '../../src/renderer/src/ui/notifications'
import { useNotify } from '../../src/renderer/src/ui/use-notify'
import zh from '../../src/renderer/src/i18n/locales/zh.json'
import { createTestI18n, renderWithI18n } from '../support/helpers'

function Publisher({
  label,
  message,
  timeout,
  tone
}: {
  label: string
  message: string
  timeout?: number
  tone?: string
}): React.JSX.Element {
  const notify = useNotify()
  return (
    <button type="button" onClick={() => notify(message, { timeout, tone })}>
      {label}
    </button>
  )
}

function scope(children: ReactNode): React.JSX.Element {
  return (
    <StrictMode>
      <NotificationProvider>{children}</NotificationProvider>
    </StrictMode>
  )
}

async function advanceTime(milliseconds: number): Promise<void> {
  await act(async () => {
    await vi.advanceTimersByTimeAsync(milliseconds)
  })
}

afterEach(() => {
  vi.useRealTimers()
  vi.restoreAllMocks()
})

describe('Application notifications', () => {
  it('shares one notification across publishers and replaces repeated messages in place', async () => {
    renderWithI18n(
      scope(
        <>
          <Publisher label="First source" message="First message" />
          <Publisher label="Second source" message="Second message" tone="error" />
        </>
      )
    )
    const user = userEvent.setup()
    await user.click(screen.getByRole('button', { name: 'First source' }))
    const notification = screen.getByRole('status')
    expect(notification).toHaveTextContent('First message')
    await user.click(screen.getByRole('button', { name: 'Second source' }))
    expect(screen.getAllByRole('status')).toHaveLength(1)
    expect(notification).toHaveTextContent('Second message')
    expect(notification).toHaveAttribute('data-type', 'error')
    await user.click(screen.getByRole('button', { name: 'Second source' }))
    expect(screen.getAllByRole('status')).toHaveLength(1)
    expect(screen.getByRole('status')).toBe(notification)
    expect(document.querySelectorAll('[data-bui-toast-viewport]')).toHaveLength(1)
  })

  it('can publish the same message again after manual dismissal', async () => {
    renderWithI18n(scope(<Publisher label="Publish" message="Repeatable" />))
    const user = userEvent.setup()
    await user.click(screen.getByRole('button', { name: 'Publish' }))
    await user.click(within(screen.getByRole('status')).getByRole('button', { name: 'Dismiss' }))
    await waitFor(() => expect(screen.queryByRole('status')).not.toBeInTheDocument())
    await user.click(screen.getByRole('button', { name: 'Publish' }))
    expect(screen.getAllByRole('status')).toHaveLength(1)
    expect(screen.getByRole('status')).toHaveTextContent('Repeatable')
  })

  it('keeps a replacement published during the previous notification exit', async () => {
    renderWithI18n(
      scope(
        <>
          <Publisher label="First" message="Old message" />
          <Publisher label="Next" message="New message" />
        </>
      )
    )
    fireEvent.click(screen.getByRole('button', { name: 'First' }))
    const previous = screen.getByRole('status')
    let finishExit: (() => void) | undefined
    const finished = new Promise<void>((resolve) => {
      finishExit = resolve
    })
    Object.defineProperty(previous, 'getAnimations', {
      configurable: true,
      value: () => [
        {
          effect: { getTiming: () => ({ duration: 120, iterations: 1 }) },
          finished,
          pending: true,
          playState: 'running'
        }
      ]
    })
    fireEvent.click(within(previous).getByRole('button', { name: 'Dismiss' }))
    expect(previous).toHaveAttribute('data-ending-style')
    fireEvent.click(screen.getByRole('button', { name: 'Next' }))
    await act(async () => {
      finishExit?.()
      await finished
    })
    await waitFor(() => expect(screen.getByRole('status')).toHaveTextContent('New message'))
    expect(screen.getAllByRole('status')).toHaveLength(1)
  })

  it('requires manual dismissal by default even after replacing an expiring message', async () => {
    vi.useFakeTimers()
    renderWithI18n(
      scope(
        <>
          <Publisher label="Timed" message="Timed message" timeout={1000} />
          <Publisher label="Persistent" message="Persistent message" />
        </>
      )
    )
    fireEvent.click(screen.getByRole('button', { name: 'Timed' }))
    await advanceTime(500)
    fireEvent.click(screen.getByRole('button', { name: 'Persistent' }))
    await advanceTime(60_000)
    expect(screen.getByRole('status')).toHaveTextContent('Persistent message')
    expect(screen.getAllByRole('status')).toHaveLength(1)
  })

  it('refreshes an explicit timeout on replacement and dismisses only the current message', async () => {
    vi.useFakeTimers()
    renderWithI18n(
      scope(
        <>
          <Publisher label="First" message="First message" timeout={1000} />
          <Publisher label="Next" message="Next message" timeout={2000} />
        </>
      )
    )
    fireEvent.click(screen.getByRole('button', { name: 'First' }))
    await advanceTime(500)
    fireEvent.click(screen.getByRole('button', { name: 'Next' }))
    await advanceTime(600)
    expect(screen.getByRole('status')).toHaveTextContent('Next message')
    fireEvent.click(screen.getByRole('button', { name: 'Next' }))
    await advanceTime(1500)
    expect(screen.getByRole('status')).toHaveTextContent('Next message')
    await advanceTime(600)
    expect(screen.queryByRole('status')).not.toBeInTheDocument()
  })

  it('updates the dismiss label when the application language changes', async () => {
    const language = createTestI18n()
    language.addResourceBundle('zh', 'translation', zh)
    renderWithI18n(
      <I18nextProvider i18n={language}>
        {scope(<Publisher label="Publish" message="An existing message" />)}
      </I18nextProvider>
    )
    await userEvent.click(screen.getByRole('button', { name: 'Publish' }))
    expect(
      within(screen.getByRole('status')).getByRole('button', { name: 'Dismiss' })
    ).toBeVisible()
    await act(async () => {
      await language.changeLanguage('zh')
    })
    expect(
      within(screen.getByRole('status')).getByRole('button', { name: zh.common.dismiss })
    ).toBeVisible()
    expect(screen.getAllByRole('status')).toHaveLength(1)
  })

  it('clears its portal and timers on unmount, ignoring a retained publisher callback', async () => {
    vi.useFakeTimers()
    let publishLater: ReturnType<typeof useNotify> | undefined
    function Capture(): React.JSX.Element {
      publishLater = useNotify()
      return <Publisher label="Publish" message="Old root" timeout={1000} />
    }
    const first = renderWithI18n(scope(<Capture />))
    fireEvent.click(screen.getByRole('button', { name: 'Publish' }))
    first.unmount()
    expect(screen.queryByRole('status')).not.toBeInTheDocument()
    expect(document.querySelector('[data-bui-toast-viewport]')).toBeNull()
    const next = renderWithI18n(scope(<Publisher label="Publish again" message="New root" />))
    fireEvent.click(screen.getByRole('button', { name: 'Publish again' }))
    await advanceTime(100)
    const timersBeforeLateResult = vi.getTimerCount()
    act(() => publishLater?.('Late old result', { timeout: 50 }))
    expect(vi.getTimerCount()).toBe(timersBeforeLateResult)
    await advanceTime(5000)
    expect(screen.getByRole('status')).toHaveTextContent('New root')
    next.unmount()
    await advanceTime(5000)
    expect(screen.queryByRole('status')).not.toBeInTheDocument()
  })
})

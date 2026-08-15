// @vitest-environment jsdom
import { describe, expect, it, vi } from 'vitest'
import { act, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import App from '../../src/renderer/src/App'
import { emitSessionEvent, installFakeApi, renderWithI18n, type FakeApi } from './helpers'
import type { AppSettings, HostConfig } from '../../src/shared/types'

/**
 * App-level connect flow (S5.1): connect, password prompt on AUTH_FAILED,
 * host-key confirm + retry, reconnect, and tab switching. The whole App runs
 * unmocked — real App state machine, useSessions, SessionTabs and the modals —
 * against a typed fake window.api. Only the heavy terminal library is stubbed
 * (see below): xterm.js needs real canvas measurement, which jsdom cannot
 * provide, so the Terminal/FitAddon are inert doubles.
 */
vi.mock('@xterm/xterm', () => {
  class MockTerminal {
    cols = 80
    rows = 24
    options: Record<string, unknown> = {}
    loadAddon = vi.fn()
    open = vi.fn()
    write = vi.fn()
    dispose = vi.fn()
    onData = vi.fn(() => ({ dispose: vi.fn() }))
  }
  return { Terminal: MockTerminal }
})
vi.mock('@xterm/addon-fit', () => {
  class MockFitAddon {
    fit = vi.fn()
  }
  return { FitAddon: MockFitAddon }
})

function defaultSettings(): AppSettings {
  return {
    language: 'en',
    themePreference: 'system',
    terminalFontFamily: 'Hack',
    terminalFontSize: 14,
    mcpIdleTimeoutMinutes: 10,
    mcpMaxSessions: 8
  }
}

function makeHost(overrides: Partial<HostConfig> = {}): HostConfig {
  return {
    id: 'h1',
    name: 'My server',
    host: 'example.com',
    port: 22,
    username: 'azure',
    authMethod: 'password',
    credentialsSaved: true,
    credentialsPrompted: true,
    ...overrides
  }
}

/** Backend rejection exactly as the Wails adapter surfaces it (NODESHELL_ERR:<code>:<msg>). */
function backendError(code: string, message: string): Error {
  return new Error(`NODESHELL_ERR:${code}:${message}`)
}

async function renderApp(hosts: HostConfig[]): Promise<FakeApi> {
  const fake = installFakeApi()
  fake.mocks.hosts.list.mockResolvedValue(hosts)
  fake.mocks.settings.get.mockResolvedValue(defaultSettings())
  renderWithI18n(<App />)
  return fake
}

function openHostsPicker(user: ReturnType<typeof userEvent.setup>): Promise<void> {
  return user.click(screen.getByRole('button', { name: 'Hosts' }))
}

function hostRow(name: string): HTMLElement {
  const node = screen.getByText(name, { selector: '.host-item-name' }).closest('li')
  if (!node) throw new Error(`host row for "${name}" not found`)
  return node as HTMLElement
}

function passwordModal(): HTMLElement {
  const el = document.querySelector('.password-modal')
  if (!el) throw new Error('password modal not found')
  return el as HTMLElement
}

describe('App connect flow', () => {
  it('connects a saved-password host: sessions.connect then an active tab appears', async () => {
    const fake = await renderApp([makeHost()])
    fake.mocks.sessions.connect.mockResolvedValue({ sessionId: 's1' })
    const user = userEvent.setup()

    await openHostsPicker(user)
    await user.click(within(hostRow('My server')).getByRole('button', { name: 'Connect' }))

    await waitFor(() => expect(fake.mocks.sessions.connect).toHaveBeenCalledWith('h1', undefined))

    const tab = await screen.findByRole('tab', { name: /My server/ })
    expect(tab).toHaveAttribute('aria-selected', 'true')
    expect(screen.queryByRole('dialog')).not.toBeInTheDocument()
  })

  it('AUTH_FAILED opens the password prompt; submit retries sessions.connect with the password', async () => {
    const fake = await renderApp([makeHost()])
    fake.mocks.sessions.connect
      .mockRejectedValueOnce(backendError('AUTH_FAILED', 'authentication failed'))
      .mockResolvedValueOnce({ sessionId: 's2' })
    const user = userEvent.setup()

    await openHostsPicker(user)
    await user.click(within(hostRow('My server')).getByRole('button', { name: 'Connect' }))

    expect(
      await screen.findByRole('heading', { name: 'Password for My server' })
    ).toBeInTheDocument()
    expect(within(passwordModal()).getByRole('alert')).toHaveTextContent(
      'Authentication failed: incorrect username or password'
    )

    await user.type(screen.getByLabelText('Password'), 'pw1')
    await user.click(within(passwordModal()).getByRole('button', { name: 'Connect' }))

    await waitFor(() =>
      expect(fake.mocks.sessions.connect).toHaveBeenNthCalledWith(2, 'h1', { password: 'pw1' })
    )
    const tab = await screen.findByRole('tab', { name: /My server/ })
    expect(tab).toHaveAttribute('aria-selected', 'true')
  })

  it('cancel during an in-flight password connect aborts from the terminal connecting pane', async () => {
    const fake = await renderApp([makeHost({ credentialsSaved: false })])
    let rejectConnect: ((e: Error) => void) | null = null
    fake.mocks.sessions.connect.mockImplementation(
      () =>
        new Promise((_resolve, reject) => {
          rejectConnect = reject
        })
    )
    // Faithful to the Go manager (internal/sessions): CancelConnect aborts the
    // in-flight connect, whose ctx cancellation rejects with CANCELLED.
    fake.mocks.sessions.cancelConnect.mockImplementation(() => {
      rejectConnect?.(backendError('CANCELLED', 'Connection cancelled'))
    })
    const user = userEvent.setup()

    await openHostsPicker(user)
    await user.click(within(hostRow('My server')).getByRole('button', { name: 'Connect' }))

    // Password host without saved credentials prompts first, no connect yet.
    await screen.findByRole('heading', { name: 'Password for My server' })
    expect(fake.mocks.sessions.connect).not.toHaveBeenCalled()

    await user.type(screen.getByLabelText('Password'), 'pw1')
    await user.click(within(passwordModal()).getByRole('button', { name: 'Connect' }))

    // Host picker and password modal close; connecting status lives on the tab.
    await waitFor(() => expect(screen.queryByRole('dialog')).not.toBeInTheDocument())
    expect(await screen.findByRole('status')).toHaveTextContent(/Connecting to My server/)
    expect(fake.mocks.sessions.connect).toHaveBeenCalledTimes(1)

    await user.click(screen.getByRole('button', { name: 'Cancel' }))

    expect(fake.mocks.sessions.cancelConnect).toHaveBeenCalledTimes(1)

    // The cancelled connect yields no session, so no session tab may ever
    // appear. Scope to the session tab bar so the Agent toggle (a button, not
    // a tab) cannot be mistaken for a session.
    const tabBar = document.querySelector('.session-tab-bar') as HTMLElement
    await waitFor(() => expect(within(tabBar).queryByRole('tab')).not.toBeInTheDocument())
    expect(within(tabBar).getByRole('button', { name: 'Hide Agent' })).toHaveAttribute(
      'aria-pressed',
      'true'
    )
  })

  it('HOST_KEY_UNKNOWN shows the fingerprint; confirm retries with acceptHostKey, password preserved', async () => {
    const fake = await renderApp([makeHost({ credentialsSaved: false })])
    fake.mocks.sessions.connect
      .mockRejectedValueOnce(
        backendError('HOST_KEY_UNKNOWN', 'Unknown host key (SHA256:AbCdEf1234567890/AbC=+)')
      )
      .mockResolvedValueOnce({ sessionId: 's3' })
    const user = userEvent.setup()

    await openHostsPicker(user)
    await user.click(within(hostRow('My server')).getByRole('button', { name: 'Connect' }))
    await screen.findByRole('heading', { name: 'Password for My server' })
    await user.type(screen.getByLabelText('Password'), 'pw1')
    await user.click(within(passwordModal()).getByRole('button', { name: 'Connect' }))

    expect(await screen.findByRole('heading', { name: 'Unknown host key' })).toBeInTheDocument()
    expect(screen.getByText(/AbCdEf1234567890\/AbC=\+/)).toBeInTheDocument()

    await user.click(screen.getByRole('button', { name: 'OK' }))

    await waitFor(() =>
      expect(fake.mocks.sessions.connect).toHaveBeenNthCalledWith(2, 'h1', {
        password: 'pw1',
        acceptHostKey: true
      })
    )
    const tab = await screen.findByRole('tab', { name: /My server/ })
    expect(tab).toHaveAttribute('aria-selected', 'true')
  })

  it('HOST_KEY_CHANGED warns, shows the NEW fingerprint, and confirm retries with acceptHostKey', async () => {
    const fake = await renderApp([makeHost()])
    fake.mocks.sessions.connect
      .mockRejectedValueOnce(
        backendError(
          'HOST_KEY_CHANGED',
          'Host key has changed (was SHA256:AbCdEf1234567890/AbC=+, now SHA256:QqWwEeRr1234/5678=)'
        )
      )
      .mockResolvedValueOnce({ sessionId: 's4' })
    const user = userEvent.setup()

    await openHostsPicker(user)
    await user.click(within(hostRow('My server')).getByRole('button', { name: 'Connect' }))

    expect(await screen.findByRole('heading', { name: 'Host key changed' })).toBeInTheDocument()
    // The security-critical fingerprint is the NEW key the user is asked to
    // accept — the replaced OLD key must not be shown as the fingerprint.
    expect(screen.getByText(/QqWwEeRr1234\/5678=/)).toBeInTheDocument()
    expect(screen.queryByText(/AbCdEf1234567890\/AbC=\+/)).not.toBeInTheDocument()

    await user.click(screen.getByRole('button', { name: 'OK' }))

    await waitFor(() =>
      expect(fake.mocks.sessions.connect).toHaveBeenNthCalledWith(2, 'h1', { acceptHostKey: true })
    )
    await screen.findByRole('tab', { name: /My server/ })
  })

  it('reconnects a session disconnected by the backend', async () => {
    const fake = await renderApp([makeHost()])
    fake.mocks.sessions.connect
      .mockResolvedValueOnce({ sessionId: 's1' })
      .mockResolvedValueOnce({ sessionId: 's2' })
    const user = userEvent.setup()

    await openHostsPicker(user)
    await user.click(within(hostRow('My server')).getByRole('button', { name: 'Connect' }))
    await screen.findByRole('tab', { name: /My server/ })

    // Backend closes the session → disconnected banner with a Reconnect action.
    await act(async () => {
      emitSessionEvent(fake, 'closed', { sessionId: 's1' })
    })
    expect(await screen.findByRole('alert')).toHaveTextContent('Session disconnected')

    await user.click(screen.getByRole('button', { name: 'Reconnect' }))

    await waitFor(() => expect(fake.mocks.sessions.connect).toHaveBeenCalledTimes(2))
    expect(fake.mocks.sessions.connect).toHaveBeenNthCalledWith(2, 'h1', undefined)
    await waitFor(() => expect(fake.mocks.sessions.disconnect).toHaveBeenCalledWith('s1'))

    // Tab survives under the new session id and returns to connected state.
    await waitFor(() =>
      expect(screen.getByRole('tab', { name: /My server/ })).toHaveAttribute(
        'aria-selected',
        'true'
      )
    )
    expect(screen.queryByText('Session disconnected')).not.toBeInTheDocument()
  })

  it('shows the backend error message and a reconnect action after session:error', async () => {
    const fake = await renderApp([makeHost()])
    fake.mocks.sessions.connect.mockResolvedValue({ sessionId: 's1' })
    const user = userEvent.setup()

    await openHostsPicker(user)
    await user.click(within(hostRow('My server')).getByRole('button', { name: 'Connect' }))
    await screen.findByRole('tab', { name: /My server/ })

    await act(async () => {
      emitSessionEvent(fake, 'error', {
        sessionId: 's1',
        error: { code: 'UNKNOWN', message: 'remote reset the connection' }
      })
    })

    expect(await screen.findByRole('alert')).toHaveTextContent('remote reset the connection')
    expect(screen.getByRole('button', { name: 'Reconnect' })).toBeInTheDocument()
  })
})

describe('App tab switching', () => {
  it('switches the active tab and closing a tab disconnects the session', async () => {
    const fake = await renderApp([
      makeHost({ id: 'h1', name: 'Alpha', host: '10.0.0.1' }),
      makeHost({ id: 'h2', name: 'Beta', host: '10.0.0.2' })
    ])
    fake.mocks.sessions.connect.mockImplementation((hostId: string) =>
      Promise.resolve({ sessionId: hostId === 'h1' ? 's1' : 's2' })
    )
    const user = userEvent.setup()

    // Connect Alpha, then Beta.
    await openHostsPicker(user)
    await user.click(within(hostRow('Alpha')).getByRole('button', { name: 'Connect' }))
    const alphaTab = await screen.findByRole('tab', { name: /Alpha/ })
    expect(alphaTab).toHaveAttribute('aria-selected', 'true')

    await openHostsPicker(user)
    await user.click(within(hostRow('Beta')).getByRole('button', { name: 'Connect' }))
    const betaTab = await screen.findByRole('tab', { name: /Beta/ })
    expect(betaTab).toHaveAttribute('aria-selected', 'true')
    expect(alphaTab).toHaveAttribute('aria-selected', 'false')

    // Clicking Alpha makes it active again.
    await user.click(alphaTab)
    expect(alphaTab).toHaveAttribute('aria-selected', 'true')
    expect(betaTab).toHaveAttribute('aria-selected', 'false')

    // Closing Beta calls sessions.disconnect and removes the tab.
    await user.click(screen.getByRole('button', { name: 'Close Beta' }))
    await waitFor(() => expect(fake.mocks.sessions.disconnect).toHaveBeenCalledWith('s2'))
    expect(screen.queryByRole('tab', { name: /Beta/ })).not.toBeInTheDocument()
    expect(alphaTab).toHaveAttribute('aria-selected', 'true')
  })

  it('shows the agent dock by default and hides it from the session tab bar', async () => {
    await renderApp([])
    const user = userEvent.setup()

    expect(screen.getByRole('heading', { name: 'Agent' })).toBeVisible()
    const actions = document.querySelector('.session-tab-bar-actions') as HTMLElement
    const toggle = within(actions).getByRole('button', { name: 'Hide Agent' })
    expect(toggle).toHaveAttribute('aria-pressed', 'true')
    expect(toggle.tagName).toBe('BUTTON')

    await user.click(toggle)
    expect(screen.queryByRole('heading', { name: 'Agent' })).not.toBeInTheDocument()
    expect(document.querySelector('.agent-dock')).toHaveClass('is-collapsed')
    expect(toggle).toHaveAccessibleName('Show Agent')
    expect(toggle).toHaveAttribute('aria-pressed', 'false')

    await user.click(toggle)
    expect(screen.getByRole('heading', { name: 'Agent' })).toBeVisible()
    expect(document.querySelector('.agent-dock')).not.toHaveClass('is-collapsed')
    expect(toggle).toHaveAttribute('aria-pressed', 'true')
  })

  it('opens settings from the icon rail', async () => {
    await renderApp([])
    const user = userEvent.setup()
    const rail = document.querySelector('.icon-rail') as HTMLElement
    await user.click(within(rail).getByRole('button', { name: 'Settings' }))
    expect(await screen.findByRole('heading', { name: 'Settings' })).toBeInTheDocument()
  })

  it('shows the monitor sheet by default and collapses it from the icon rail', async () => {
    await renderApp([])
    const user = userEvent.setup()

    expect(screen.getByRole('heading', { name: 'Monitor' })).toBeVisible()
    expect(document.querySelector('.sidebar')).not.toHaveClass('is-collapsed')

    const rail = document.querySelector('.icon-rail') as HTMLElement
    const toggle = within(rail).getByRole('button', { name: 'Hide monitor' })
    expect(toggle).toHaveAttribute('aria-pressed', 'true')

    await user.click(toggle)
    expect(screen.queryByRole('heading', { name: 'Monitor' })).not.toBeInTheDocument()
    expect(document.querySelector('.sidebar')).toHaveClass('is-collapsed')
    expect(toggle).toHaveAccessibleName('Show monitor')
    expect(toggle).toHaveAttribute('aria-pressed', 'false')

    await user.click(toggle)
    expect(screen.getByRole('heading', { name: 'Monitor' })).toBeVisible()
    expect(document.querySelector('.sidebar')).not.toHaveClass('is-collapsed')
    expect(toggle).toHaveAttribute('aria-pressed', 'true')
  })
})

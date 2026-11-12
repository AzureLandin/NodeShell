// @vitest-environment jsdom
import { describe, expect, it, vi } from 'vitest'
import { act, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import App from '../../src/renderer/src/App'
import { emitSessionEvent, installFakeApi, renderWithI18n, type FakeApi } from '../support/helpers'
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
    attachCustomKeyEventHandler = vi.fn()
  }
  return { Terminal: MockTerminal }
})
vi.mock('@xterm/addon-fit', () => {
  class MockFitAddon {
    fit = vi.fn()
  }
  return { FitAddon: MockFitAddon }
})
vi.mock('@xterm/addon-webgl', () => {
  class MockWebglAddon {
    onContextLoss = vi.fn(() => ({ dispose: vi.fn() }))
    dispose = vi.fn()
  }
  return { WebglAddon: MockWebglAddon }
})
vi.mock('@xterm/addon-unicode-graphemes', () => {
  class MockUnicodeGraphemesAddon {}
  return { UnicodeGraphemesAddon: MockUnicodeGraphemesAddon }
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
    expect(screen.queryByRole('status')).not.toBeInTheDocument()

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
    expect(within(tabBar).getByRole('button', { name: 'Show Agent' })).toHaveAttribute(
      'aria-pressed',
      'false'
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

describe('App notifications', () => {
  it('reports a coded connection failure through one localized notification', async () => {
    const fake = await renderApp([makeHost()])
    fake.mocks.sessions.connect.mockRejectedValue(
      backendError('CONNECTION_REFUSED', 'dial refused')
    )
    const user = userEvent.setup()
    await openHostsPicker(user)
    await user.click(within(hostRow('My server')).getByRole('button', { name: 'Connect' }))
    expect(await screen.findByRole('status')).toHaveTextContent(
      'Connection refused: confirm example.com:22 is correct and SSH is listening'
    )
    expect(screen.getAllByRole('status')).toHaveLength(1)
    expect(screen.queryByText(/NODESHELL_ERR/)).not.toBeInTheDocument()
  })

  it('normalizes credential IPC failures before publishing them', async () => {
    const fake = await renderApp([makeHost({ credentialsPrompted: false })])
    fake.mocks.sessions.connect.mockResolvedValue({ sessionId: 's1' })
    fake.mocks.credentials.isAvailable.mockResolvedValue(true)
    fake.mocks.credentials.save.mockRejectedValue(backendError('UNKNOWN', 'credential save denied'))
    const user = userEvent.setup()
    await openHostsPicker(user)
    await user.click(within(hostRow('My server')).getByRole('button', { name: 'Connect' }))
    await screen.findByRole('heading', { name: 'Save credentials' })
    await user.click(screen.getByRole('button', { name: 'Save' }))
    expect(await screen.findByRole('status')).toHaveTextContent('credential save denied')
    expect(screen.queryByText(/NODESHELL_ERR/)).not.toBeInTheDocument()
    expect(screen.getByRole('tab', { name: 'My server' })).toHaveAttribute('aria-selected', 'true')
  })

  it('keeps a connected session and permits another credential prompt after skipping fails', async () => {
    const fake = await renderApp([makeHost({ credentialsPrompted: false })])
    fake.mocks.sessions.connect
      .mockResolvedValueOnce({ sessionId: 's1' })
      .mockResolvedValueOnce({ sessionId: 's2' })
    fake.mocks.credentials.markPrompted
      .mockRejectedValueOnce(backendError('UNKNOWN', 'credential preference denied'))
      .mockResolvedValueOnce(undefined)
    const user = userEvent.setup()
    await openHostsPicker(user)
    await user.click(within(hostRow('My server')).getByRole('button', { name: 'Connect' }))
    await screen.findByRole('heading', { name: 'Save credentials' })
    await user.click(screen.getByRole('button', { name: 'Not now' }))
    expect(await screen.findByRole('status')).toHaveTextContent('credential preference denied')
    expect(screen.queryByText(/NODESHELL_ERR/)).not.toBeInTheDocument()
    expect(screen.getByRole('tab', { name: 'My server' })).toHaveAttribute('aria-selected', 'true')
    expect(fake.mocks.sessions.disconnect).not.toHaveBeenCalled()
    await user.click(within(screen.getByRole('status')).getByRole('button', { name: 'Dismiss' }))
    await openHostsPicker(user)
    await user.click(within(hostRow('My server')).getByRole('button', { name: 'Connect' }))
    await screen.findByRole('heading', { name: 'Save credentials' })
    await user.click(screen.getByRole('button', { name: 'Not now' }))
    await waitFor(() => expect(fake.mocks.credentials.markPrompted).toHaveBeenCalledTimes(2))
    expect(fake.mocks.sessions.connect).toHaveBeenCalledTimes(2)
    expect(fake.mocks.credentials.save).not.toHaveBeenCalled()
    expect(screen.queryByRole('status')).not.toBeInTheDocument()
  })

  it('replaces messages across settings and session events, and dismisses without changing session errors', async () => {
    const fake = await renderApp([makeHost()])
    const user = userEvent.setup()
    fake.mocks.sessions.connect.mockResolvedValue({ sessionId: 's1' })
    await openHostsPicker(user)
    await user.click(within(hostRow('My server')).getByRole('button', { name: 'Connect' }))
    await screen.findByRole('tab', { name: 'My server' })
    act(() => {
      emitSessionEvent(fake, 'error', {
        sessionId: 's1',
        error: { code: 'UNKNOWN', message: 'first connection reset' }
      })
    })
    expect(await screen.findByRole('status')).toHaveTextContent('first connection reset')

    fake.mocks.settings.set.mockRejectedValue(new Error('settings write failed'))
    const rail = document.querySelector('.icon-rail') as HTMLElement
    await user.click(within(rail).getByRole('button', { name: 'Settings' }))
    await user.click(screen.getByRole('button', { name: 'General' }))
    const theme = screen.getByLabelText('Theme')
    await user.click(theme)
    await user.click(await screen.findByRole('option', { name: 'Light' }))
    await waitFor(() =>
      expect(fake.mocks.settings.set).toHaveBeenCalledWith({ themePreference: 'light' })
    )
    expect(theme).toHaveTextContent('System')
    expect(await screen.findByRole('status')).toHaveTextContent('Connection failed')
    expect(screen.getAllByRole('status')).toHaveLength(1)

    await user.click(within(screen.getByRole('dialog')).getByRole('button', { name: 'Dismiss' }))
    await waitFor(() => expect(screen.queryByRole('dialog')).not.toBeInTheDocument())
    const notification = screen.getByRole('status')
    await user.click(within(notification).getByRole('button', { name: 'Dismiss' }))
    await waitFor(() => expect(screen.queryByRole('status')).not.toBeInTheDocument())
    expect(screen.getByRole('alert')).toHaveTextContent('first connection reset')
    await user.click(within(rail).getByRole('button', { name: 'Hide monitor' }))
    expect(screen.queryByRole('status')).not.toBeInTheDocument()

    act(() => {
      emitSessionEvent(fake, 'error', {
        sessionId: 's1',
        error: { code: 'UNKNOWN', message: 'first connection reset' }
      })
    })
    expect(await screen.findByRole('status')).toHaveTextContent('first connection reset')
    expect(fake.mocks.sessions.onError).toHaveBeenCalledTimes(1)
    expect(document.querySelectorAll('[data-bui-toast-viewport]')).toHaveLength(1)
  })

  it('shows a generic reconnect failure once while preserving the existing terminal', async () => {
    const fake = await renderApp([makeHost()])
    const user = userEvent.setup()
    fake.mocks.sessions.connect.mockResolvedValueOnce({ sessionId: 's1' })
    await openHostsPicker(user)
    await user.click(within(hostRow('My server')).getByRole('button', { name: 'Connect' }))
    await screen.findByRole('tab', { name: 'My server' })
    act(() => emitSessionEvent(fake, 'closed', { sessionId: 's1' }))
    fake.mocks.sessions.connect.mockRejectedValueOnce(new Error('reconnect transport broke'))
    await user.click(screen.getByRole('button', { name: 'Reconnect' }))
    expect(await screen.findByRole('status')).toHaveTextContent('reconnect transport broke')
    expect(screen.getAllByRole('status')).toHaveLength(1)
    expect(screen.getByRole('alert')).toHaveTextContent('reconnect transport broke')
    expect(screen.getByRole('tab', { name: 'My server' })).toHaveAttribute('aria-selected', 'true')
    expect(fake.mocks.sessions.disconnect).not.toHaveBeenCalled()
  })

  it('keeps host-list failures local with explicit retry and no repeated background notification', async () => {
    const fake = installFakeApi()
    fake.mocks.hosts.list.mockRejectedValue(new Error('hosts file unreadable'))
    fake.mocks.settings.get.mockResolvedValue(defaultSettings())
    renderWithI18n(<App />)
    const user = userEvent.setup()
    await openHostsPicker(user)
    expect(await screen.findByRole('alert')).toHaveTextContent('hosts file unreadable')
    expect(screen.queryByText('No saved hosts yet')).not.toBeInTheDocument()
    expect(document.querySelector('[data-bui-toast-root]')).not.toBeInTheDocument()
    const calls = fake.mocks.hosts.list.mock.calls.length
    await user.click(within(screen.getByRole('dialog')).getByRole('button', { name: 'Dismiss' }))
    await waitFor(() => expect(screen.queryByRole('dialog')).not.toBeInTheDocument())
    const rail = document.querySelector('.icon-rail') as HTMLElement
    await user.click(within(rail).getByRole('button', { name: 'Hide monitor' }))
    expect(screen.queryByRole('status')).not.toBeInTheDocument()
    expect(fake.mocks.hosts.list).toHaveBeenCalledTimes(calls)
    fake.mocks.hosts.list.mockResolvedValue([])
    await openHostsPicker(user)
    expect(await screen.findByText('No saved hosts yet')).toBeInTheDocument()
    expect(screen.queryByRole('alert')).not.toBeInTheDocument()
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

  it('toggles the agent panel with the top-bar button', async () => {
    await renderApp([])
    const user = userEvent.setup()

    expect(document.querySelector('.agent-dock')).toHaveClass('is-collapsed')
    const actions = document.querySelector('.session-tab-bar-actions') as HTMLElement
    const toggle = within(actions).getByRole('button', { name: 'Show Agent' })
    expect(toggle).toHaveAttribute('aria-pressed', 'false')
    expect(toggle.tagName).toBe('BUTTON')

    await user.click(toggle)
    expect(screen.getByRole('heading', { name: 'Agent' })).toBeVisible()
    expect(document.querySelector('.agent-dock')).not.toHaveClass('is-collapsed')
    expect(toggle).toHaveAccessibleName('Hide Agent')
    expect(toggle).toHaveAttribute('aria-pressed', 'true')

    await user.click(toggle)
    expect(screen.queryByRole('heading', { name: 'Agent' })).not.toBeInTheDocument()
    expect(document.querySelector('.agent-dock')).toHaveClass('is-collapsed')
    expect(toggle).toHaveAttribute('aria-pressed', 'false')
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

describe('App host query flows', () => {
  it('closes a successful edit even when refresh fails, retaining the confirmed host and a local retry', async () => {
    const fake = await renderApp([makeHost()])
    const user = userEvent.setup()
    await openHostsPicker(user)
    await user.click(screen.getByText('My server', { selector: '.host-item-name' }))
    const name = screen.getByLabelText('Name')
    await user.clear(name)
    await user.type(name, 'Renamed')
    fake.mocks.hosts.update.mockResolvedValue(makeHost({ name: 'Renamed' }))
    fake.mocks.hosts.list.mockRejectedValue(backendError('CONFIG_READ_FAILED', 'refresh denied'))
    await user.click(screen.getByRole('button', { name: 'Save' }))
    await waitFor(() => expect(screen.queryByLabelText('Name')).not.toBeInTheDocument())
    expect(await screen.findByText('Renamed', { selector: '.host-item-name' })).toBeInTheDocument()
    expect(screen.getByRole('alert')).toHaveTextContent(
      'Successful operations do not need to be repeated'
    )
    expect(screen.getByRole('alert')).toHaveTextContent('refresh denied')
    expect(fake.mocks.hosts.update).toHaveBeenCalledTimes(1)
    expect(screen.queryByText(/NODESHELL_ERR/)).not.toBeInTheDocument()
    fake.mocks.hosts.list.mockResolvedValue([makeHost({ name: 'Renamed' })])
    await user.click(screen.getByRole('button', { name: 'Retry loading hosts' }))
    await waitFor(() => expect(screen.queryByRole('alert')).not.toBeInTheDocument())
    expect(fake.mocks.hosts.update).toHaveBeenCalledTimes(1)
  })

  it('uses a fresh metadata read before asking to save credentials', async () => {
    const fake = await renderApp([makeHost({ credentialsPrompted: false })])
    const user = userEvent.setup()
    await openHostsPicker(user)
    fake.mocks.hosts.list.mockResolvedValue([makeHost({ credentialsPrompted: true })])
    fake.mocks.sessions.connect.mockResolvedValue({ sessionId: 'fresh-session' })
    await user.click(within(hostRow('My server')).getByRole('button', { name: 'Connect' }))
    await screen.findByRole('tab', { name: 'My server' })
    await waitFor(() => expect(fake.mocks.hosts.list).toHaveBeenCalledTimes(3))
    expect(screen.queryByRole('heading', { name: 'Save credentials' })).not.toBeInTheDocument()
    expect(fake.mocks.credentials.save).not.toHaveBeenCalled()
  })

  it('does not recast a connected session as failed when credential metadata cannot be read', async () => {
    const fake = await renderApp([makeHost({ credentialsPrompted: false })])
    const user = userEvent.setup()
    await openHostsPicker(user)
    fake.mocks.hosts.list.mockRejectedValue(
      backendError('CONFIG_READ_FAILED', 'metadata unavailable')
    )
    fake.mocks.sessions.connect.mockResolvedValue({ sessionId: 'connected-session' })
    await user.click(within(hostRow('My server')).getByRole('button', { name: 'Connect' }))
    expect(await screen.findByRole('tab', { name: 'My server' })).toHaveAttribute(
      'aria-selected',
      'true'
    )
    await waitFor(() => expect(fake.mocks.hosts.list).toHaveBeenCalledTimes(3))
    expect(screen.queryByRole('heading', { name: 'Save credentials' })).not.toBeInTheDocument()
    expect(screen.queryByRole('status')).not.toBeInTheDocument()
    await openHostsPicker(user)
    expect(await screen.findByRole('alert')).toHaveTextContent('metadata unavailable')
    await user.click(within(screen.getByRole('dialog')).getByRole('button', { name: 'Dismiss' }))
    await waitFor(() => expect(screen.queryByRole('dialog')).not.toBeInTheDocument())
    expect(screen.getByRole('tab', { name: 'My server' })).toHaveAttribute('aria-selected', 'true')
    expect(fake.mocks.sessions.disconnect).not.toHaveBeenCalled()
  })

  it('refreshes saved credential flags without storing the password in a host payload', async () => {
    const fake = await renderApp([makeHost()])
    const user = userEvent.setup()
    await openHostsPicker(user)
    await user.click(screen.getByText('My server', { selector: '.host-item-name' }))
    await user.type(screen.getByLabelText('Password'), 'ephemeral secret')
    fake.mocks.hosts.update.mockResolvedValue(makeHost())
    fake.mocks.credentials.isAvailable.mockResolvedValue(true)
    fake.mocks.credentials.save.mockImplementation(async () => {
      fake.mocks.hosts.list.mockResolvedValue([makeHost({ credentialsSaved: true })])
    })
    const before = fake.mocks.hosts.list.mock.calls.length
    await user.click(screen.getByRole('button', { name: 'Save' }))
    await waitFor(() => expect(screen.queryByLabelText('Name')).not.toBeInTheDocument())
    expect(fake.mocks.credentials.save).toHaveBeenCalledWith('h1', { password: 'ephemeral secret' })
    expect(fake.mocks.hosts.update).toHaveBeenCalledWith('h1', {
      name: 'My server',
      host: 'example.com',
      port: 22,
      username: 'azure',
      authMethod: 'password'
    })
    expect(fake.mocks.hosts.list.mock.calls.length).toBe(before + 2)
    expect(JSON.stringify(fake.mocks.hosts.update.mock.calls)).not.toContain('ephemeral secret')
  })
})

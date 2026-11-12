// @vitest-environment jsdom
import { describe, expect, it, vi } from 'vitest'
import { act, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import App from '../../src/renderer/src/App'
import { emitSessionEvent, installFakeApi, renderWithI18n, type FakeApi } from '../support/helpers'
import type { AppSettings, HostConfig } from '../../src/shared/types'
import { burstPayloads } from '../support/terminal-payloads'

interface MockTermInstance {
  cols: number
  rows: number
  options: Record<string, unknown>
  loadAddon: ReturnType<typeof vi.fn>
  open: ReturnType<typeof vi.fn>
  write: ReturnType<typeof vi.fn>
  dispose: ReturnType<typeof vi.fn>
  onData: ReturnType<typeof vi.fn>
  container: HTMLElement | null
}

const mockInstances: MockTermInstance[] = []

vi.mock('@xterm/xterm', () => {
  class MockTerminal {
    cols = 80
    rows = 24
    options: Record<string, unknown> = {}
    loadAddon = vi.fn()
    open = vi.fn((el: HTMLElement) => {
      this.container = el
    })
    write = vi.fn()
    dispose = vi.fn()
    onData = vi.fn(() => ({ dispose: vi.fn() }))
    attachCustomKeyEventHandler = vi.fn()
    container: HTMLElement | null = null

    constructor() {
      mockInstances.push(this as unknown as MockTermInstance)
    }
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
    name: 'Server 1',
    host: '10.0.0.1',
    port: 22,
    username: 'azure',
    authMethod: 'password',
    credentialsSaved: true,
    credentialsPrompted: true,
    ...overrides
  }
}

async function renderApp(hosts: HostConfig[]): Promise<FakeApi> {
  mockInstances.length = 0
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

async function connectHost(
  user: ReturnType<typeof userEvent.setup>,
  name: string
): Promise<HTMLElement> {
  await openHostsPicker(user)
  await user.click(within(hostRow(name)).getByRole('button', { name: 'Connect' }))
  const tab = await screen.findByRole('tab', { name })
  await waitFor(() => expect(tab).toHaveAttribute('aria-selected', 'true'))
  return tab
}

function threeHosts(): HostConfig[] {
  return [
    makeHost({ id: 'h1', name: 'Alpha' }),
    makeHost({ id: 'h2', name: 'Beta' }),
    makeHost({ id: 'h3', name: 'Gamma' })
  ]
}

describe('Session Terminal Persistence', () => {
  it('preserves 800 mixed output chunks across four sessions, tab switches and disposal', async () => {
    const names = ['Load Alpha', 'Load Beta', 'Load Gamma', 'Load Delta']
    const hosts = names.map((name, index) => makeHost({ id: `load-${index}`, name }))
    const fake = await renderApp(hosts)
    fake.mocks.sessions.connect.mockImplementation(async (id: string) => ({ sessionId: id }))
    const user = userEvent.setup()
    const tabs: HTMLElement[] = []
    for (const name of names) tabs.push(await connectHost(user, name))
    const terminals = [...mockInstances]
    const payloads = names.map((_name, index) => burstPayloads(`session-${index}`, 200))
    for (let round = 0; round < 8; round += 1) {
      act(() => {
        for (let index = 0; index < hosts.length; index += 1) {
          for (const data of payloads[index].slice(round * 25, (round + 1) * 25)) {
            emitSessionEvent(fake, 'data', { sessionId: hosts[index].id, data })
          }
        }
      })
      await user.click(tabs[round % tabs.length])
    }
    await waitFor(() => {
      for (let index = 0; index < terminals.length; index += 1) {
        expect(terminals[index].write.mock.calls.map(([data]) => data).join('')).toBe(
          payloads[index].join('')
        )
      }
    })
    expect(mockInstances).toHaveLength(4)
    expect(fake.mocks.sessions.onData).toHaveBeenCalledTimes(1)
    for (const terminal of terminals) expect(terminal.dispose).not.toHaveBeenCalled()
    for (const name of names)
      await user.click(screen.getByRole('button', { name: `Close ${name}` }))
    for (const terminal of terminals) expect(terminal.dispose).toHaveBeenCalledTimes(1)
    const writes = terminals.map((terminal) => terminal.write.mock.calls.length)
    act(() => {
      for (const host of hosts) emitSessionEvent(fake, 'data', { sessionId: host.id, data: 'late' })
    })
    expect(terminals.map((terminal) => terminal.write.mock.calls.length)).toEqual(writes)
  }, 15000)

  it('retains xterm instances and does not dispose when switching between tabs', async () => {
    const fake = await renderApp([
      makeHost({ id: 'h1', name: 'Alpha', host: '10.0.0.1' }),
      makeHost({ id: 'h2', name: 'Beta', host: '10.0.0.2' })
    ])
    fake.mocks.sessions.connect.mockImplementation((hostId: string) =>
      Promise.resolve({ sessionId: hostId === 'h1' ? 's1' : 's2' })
    )
    const user = userEvent.setup()

    // 1. Connect Alpha
    await openHostsPicker(user)
    await user.click(within(hostRow('Alpha')).getByRole('button', { name: 'Connect' }))
    const alphaTab = await screen.findByRole('tab', { name: /Alpha/ })
    expect(alphaTab).toHaveAttribute('aria-selected', 'true')

    await waitFor(() => expect(mockInstances.length).toBe(1))
    const termA = mockInstances[0]!
    expect(termA.open).toHaveBeenCalled()
    expect(termA.dispose).not.toHaveBeenCalled()

    // Write some output to Alpha
    act(() => {
      emitSessionEvent(fake, 'data', { sessionId: 's1', data: 'alpha-initial-output' })
    })
    await waitFor(() => expect(termA.write).toHaveBeenCalledWith('alpha-initial-output'))

    // 2. Connect Beta
    await openHostsPicker(user)
    await user.click(within(hostRow('Beta')).getByRole('button', { name: 'Connect' }))
    const betaTab = await screen.findByRole('tab', { name: /Beta/ })
    expect(betaTab).toHaveAttribute('aria-selected', 'true')
    expect(alphaTab).toHaveAttribute('aria-selected', 'false')

    await waitFor(() => expect(mockInstances.length).toBe(2))
    const termB = mockInstances[1]!
    expect(termB.open).toHaveBeenCalled()
    expect(termB.dispose).not.toHaveBeenCalled()

    // Ensure termA was NOT disposed when Beta became active
    expect(termA.dispose).not.toHaveBeenCalled()

    // Write output to Beta
    act(() => {
      emitSessionEvent(fake, 'data', { sessionId: 's2', data: 'beta-initial-output' })
    })
    await waitFor(() => expect(termB.write).toHaveBeenCalledWith('beta-initial-output'))

    // 3. Switch back to Alpha
    await user.click(alphaTab)
    expect(alphaTab).toHaveAttribute('aria-selected', 'true')
    expect(betaTab).toHaveAttribute('aria-selected', 'false')

    // No new instance should be created and neither term should be disposed
    expect(mockInstances.length).toBe(2)
    expect(termA.dispose).not.toHaveBeenCalled()
    expect(termB.dispose).not.toHaveBeenCalled()

    // 4. Repeated tab switching (10 times)
    for (let i = 0; i < 5; i++) {
      await user.click(betaTab)
      expect(betaTab).toHaveAttribute('aria-selected', 'true')
      await user.click(alphaTab)
      expect(alphaTab).toHaveAttribute('aria-selected', 'true')
    }

    expect(mockInstances.length).toBe(2)
    expect(termA.dispose).not.toHaveBeenCalled()
    expect(termB.dispose).not.toHaveBeenCalled()
    expect(termA.onData).toHaveBeenCalledTimes(1)
    expect(termB.onData).toHaveBeenCalledTimes(1)
    expect(fake.mocks.sessions.onData).toHaveBeenCalledTimes(1)
  })

  it('navigates session tabs with arrows and Home/End while keeping associated panels mounted', async () => {
    const fake = await renderApp(threeHosts())
    fake.mocks.sessions.connect.mockImplementation((hostId: string) =>
      Promise.resolve({ sessionId: hostId })
    )
    const user = userEvent.setup()
    const alpha = await connectHost(user, 'Alpha')
    const beta = await connectHost(user, 'Beta')
    const gamma = await connectHost(user, 'Gamma')
    const list = screen.getByRole('tablist', { name: 'Sessions' })
    const panels = [alpha, beta, gamma].map((tab) => {
      expect(tab.tagName).toBe('BUTTON')
      expect(tab.querySelector('button')).toBeNull()
      const panel = document.getElementById(tab.getAttribute('aria-controls')!)!
      expect(panel).toHaveAttribute('role', 'tabpanel')
      expect(panel).toHaveAttribute('aria-labelledby', tab.id)
      return panel
    })
    for (const selector of [
      '.hosts-launcher',
      '.session-tab-add',
      '.session-transfer-toggle',
      '.session-agent-toggle'
    ]) {
      expect(list.contains(document.querySelector(selector))).toBe(false)
    }
    expect(panels[0]).toHaveAttribute('hidden')
    expect(panels[0]).toHaveAttribute('inert')
    expect(panels[2]).not.toHaveAttribute('hidden')

    await user.click(alpha)
    await user.keyboard('{ArrowRight}')
    await waitFor(() => expect(beta).toHaveFocus())
    expect(beta).toHaveAttribute('aria-selected', 'true')
    expect(beta).toHaveAttribute('tabindex', '0')
    expect(alpha).toHaveAttribute('tabindex', '-1')
    await user.keyboard('{End}')
    await waitFor(() => expect(gamma).toHaveFocus())
    expect(gamma).toHaveAttribute('aria-selected', 'true')
    await user.keyboard('{Home}')
    await waitFor(() => expect(alpha).toHaveFocus())
    expect(alpha).toHaveAttribute('aria-selected', 'true')
    await user.keyboard('{ArrowLeft}')
    await waitFor(() => expect(gamma).toHaveFocus())
    expect(gamma).toHaveAttribute('aria-selected', 'true')
    for (const panel of panels) expect(panel).toBeInTheDocument()
    expect(mockInstances).toHaveLength(3)
    for (const term of mockInstances) {
      expect(term.dispose).not.toHaveBeenCalled()
      expect(term.onData).toHaveBeenCalledTimes(1)
    }
  })

  it.each([
    ['Alpha', 'Gamma', 0],
    ['Beta', 'Gamma', 1],
    ['Gamma', 'Beta', 2]
  ] as const)(
    'closes active %s and preserves the existing last-remaining selection rule',
    async (name, next, index) => {
      const fake = await renderApp(threeHosts())
      fake.mocks.sessions.connect.mockImplementation((hostId: string) =>
        Promise.resolve({ sessionId: hostId })
      )
      const user = userEvent.setup()
      for (const host of ['Alpha', 'Beta', 'Gamma']) await connectHost(user, host)
      await user.click(screen.getByRole('tab', { name }))
      const close = screen.getByRole('button', { name: `Close ${name}` })
      act(() => close.focus())
      await user.keyboard('{Enter}')
      await waitFor(() => expect(screen.queryByRole('tab', { name })).not.toBeInTheDocument())
      const nextTab = screen.getByRole('tab', { name: next })
      expect(nextTab).toHaveAttribute('aria-selected', 'true')
      expect(nextTab).toHaveFocus()
      expect(mockInstances[index]!.dispose).toHaveBeenCalledTimes(1)
      mockInstances.forEach((term, otherIndex) => {
        if (otherIndex !== index) expect(term.dispose).not.toHaveBeenCalled()
      })
    }
  )

  it('closes inactive tabs without changing selection and focuses Hosts after the final tab closes', async () => {
    const fake = await renderApp(threeHosts())
    fake.mocks.sessions.connect.mockImplementation((hostId: string) =>
      Promise.resolve({ sessionId: hostId })
    )
    const user = userEvent.setup()
    for (const host of ['Alpha', 'Beta', 'Gamma']) await connectHost(user, host)
    for (const name of ['Alpha', 'Beta']) {
      await user.click(screen.getByRole('button', { name: `Close ${name}` }))
      await waitFor(() => expect(screen.queryByRole('tab', { name })).not.toBeInTheDocument())
      expect(screen.getByRole('tab', { name: 'Gamma' })).toHaveAttribute('aria-selected', 'true')
      expect(screen.getByRole('tab', { name: 'Gamma' })).toHaveFocus()
    }
    await user.click(screen.getByRole('button', { name: 'Close Gamma' }))
    await waitFor(() =>
      expect(
        within(screen.getByRole('tablist', { name: 'Sessions' })).queryAllByRole('tab')
      ).toHaveLength(0)
    )
    expect(screen.getByRole('button', { name: 'Hosts' })).toHaveFocus()
    for (const term of mockInstances) expect(term.dispose).toHaveBeenCalledTimes(1)
  })

  it('does not take focus from a dialog opened while closing awaits IPC', async () => {
    const fake = await renderApp([makeHost({ name: 'Alpha' })])
    fake.mocks.sessions.connect.mockResolvedValue({ sessionId: 's1' })
    let finishClose!: () => void
    fake.mocks.sessions.disconnect.mockImplementation(
      () =>
        new Promise<void>((resolve) => {
          finishClose = resolve
        })
    )
    const user = userEvent.setup()
    await connectHost(user, 'Alpha')
    await user.click(screen.getByRole('button', { name: 'Close Alpha' }))
    await openHostsPicker(user)
    const dialog = await screen.findByRole('dialog')
    await user.click(within(dialog).getByRole('textbox'))
    const focused = document.activeElement
    expect(dialog.contains(focused)).toBe(true)
    await act(async () => finishClose())
    await waitFor(() =>
      expect(screen.queryByRole('tab', { name: 'Alpha' })).not.toBeInTheDocument()
    )
    expect(document.activeElement).toBe(focused)
  })

  it('routes background output to inactive session terminal instance', async () => {
    const fake = await renderApp([
      makeHost({ id: 'h1', name: 'Alpha', host: '10.0.0.1' }),
      makeHost({ id: 'h2', name: 'Beta', host: '10.0.0.2' })
    ])
    fake.mocks.sessions.connect.mockImplementation((hostId: string) =>
      Promise.resolve({ sessionId: hostId === 'h1' ? 's1' : 's2' })
    )
    const user = userEvent.setup()

    await openHostsPicker(user)
    await user.click(within(hostRow('Alpha')).getByRole('button', { name: 'Connect' }))
    await screen.findByRole('tab', { name: /Alpha/ })

    await openHostsPicker(user)
    await user.click(within(hostRow('Beta')).getByRole('button', { name: 'Connect' }))
    await screen.findByRole('tab', { name: /Beta/ })

    expect(mockInstances.length).toBe(2)
    const termA = mockInstances[0]!
    const termB = mockInstances[1]!

    // While Beta is active, send background output to Alpha (s1)
    act(() => {
      emitSessionEvent(fake, 'data', { sessionId: 's1', data: 'alpha-background-chunk' })
    })

    await waitFor(() => expect(termA.write).toHaveBeenCalledWith('alpha-background-chunk'))
    expect(termB.write).not.toHaveBeenCalledWith('alpha-background-chunk')
    expect(termA.dispose).not.toHaveBeenCalled()
  })

  it('closes a pending tab without mounting a terminal or releasing an existing terminal', async () => {
    const fake = await renderApp([
      makeHost({ id: 'h1', name: 'Alpha' }),
      makeHost({ id: 'h2', name: 'SlowServer' })
    ])
    fake.mocks.sessions.connect.mockImplementation((hostId: string) =>
      hostId === 'h1' ? Promise.resolve({ sessionId: 's1' }) : new Promise(() => {})
    )
    const user = userEvent.setup()
    const alpha = await connectHost(user, 'Alpha')
    const term = mockInstances[0]!
    const pending = await connectHost(user, 'SlowServer')
    const pendingPanel = document.getElementById(pending.getAttribute('aria-controls')!)!
    expect(pendingPanel).toHaveAttribute('aria-labelledby', pending.id)
    expect(within(pendingPanel).getByRole('status')).toBeInTheDocument()
    expect(mockInstances).toHaveLength(1)
    await user.click(screen.getByRole('button', { name: 'Close SlowServer' }))
    await waitFor(() =>
      expect(screen.queryByRole('tab', { name: 'SlowServer' })).not.toBeInTheDocument()
    )
    expect(fake.mocks.sessions.cancelConnect).toHaveBeenCalledTimes(1)
    expect(fake.mocks.sessions.disconnect).not.toHaveBeenCalled()
    expect(alpha).toHaveAttribute('aria-selected', 'true')
    expect(alpha).toHaveFocus()
    expect(term.dispose).not.toHaveBeenCalled()
    expect(mockInstances).toHaveLength(1)
  })

  it('retains the old terminal through a slow failed reconnect and replaces it only after success', async () => {
    const fake = await renderApp([makeHost({ name: 'Alpha' })])
    let failReconnect!: (error: Error) => void
    fake.mocks.sessions.connect
      .mockResolvedValueOnce({ sessionId: 's1' })
      .mockImplementationOnce(
        () =>
          new Promise((_resolve, reject) => {
            failReconnect = reject
          })
      )
      .mockResolvedValueOnce({ sessionId: 's2' })
    const user = userEvent.setup()
    const tab = await connectHost(user, 'Alpha')
    const panel = document.getElementById(tab.getAttribute('aria-controls')!)!
    const term = mockInstances[0]!
    act(() => emitSessionEvent(fake, 'closed', { sessionId: 's1' }))
    await user.click(await screen.findByRole('button', { name: 'Reconnect' }))
    expect(within(panel).getByRole('status')).toBeInTheDocument()
    expect(term.container).toBeInTheDocument()
    expect(term.dispose).not.toHaveBeenCalled()
    act(() => emitSessionEvent(fake, 'data', { sessionId: 's1', data: 'retained-tail' }))
    await waitFor(() => expect(term.write).toHaveBeenCalledWith('retained-tail'))
    await act(async () => failReconnect(new Error('Reconnect unavailable')))
    expect(await screen.findByRole('button', { name: 'Reconnect' })).toBeInTheDocument()
    expect(document.getElementById(tab.getAttribute('aria-controls')!)).toBe(panel)
    expect(mockInstances).toHaveLength(1)
    expect(term.dispose).not.toHaveBeenCalled()
    await user.click(screen.getByRole('button', { name: 'Reconnect' }))
    await waitFor(() => expect(mockInstances).toHaveLength(2))
    expect(term.dispose).toHaveBeenCalledTimes(1)
    expect(mockInstances[1]!.dispose).not.toHaveBeenCalled()
  })

  it('keeps existing terminal instances mounted during a slow pending connection', async () => {
    const fake = await renderApp([
      makeHost({ id: 'h1', name: 'Alpha', host: '10.0.0.1' }),
      makeHost({ id: 'h2', name: 'SlowServer', host: '10.0.0.3' })
    ])
    let resolveSlowConnect!: (res: { sessionId: string }) => void
    fake.mocks.sessions.connect.mockImplementation((hostId: string) => {
      if (hostId === 'h1') return Promise.resolve({ sessionId: 's1' })
      return new Promise((resolve) => {
        resolveSlowConnect = resolve
      })
    })
    const user = userEvent.setup()

    await openHostsPicker(user)
    await user.click(within(hostRow('Alpha')).getByRole('button', { name: 'Connect' }))
    await screen.findByRole('tab', { name: /Alpha/ })

    expect(mockInstances.length).toBe(1)
    const termA = mockInstances[0]!

    // Start connecting to SlowServer
    await openHostsPicker(user)
    await user.click(within(hostRow('SlowServer')).getByRole('button', { name: 'Connect' }))

    // Connecting UI appears
    expect(await screen.findByRole('status')).toBeInTheDocument()
    expect(screen.getByText(/Connecting to SlowServer/)).toBeInTheDocument()

    // Alpha's terminal must NOT be disposed during this connecting state
    expect(termA.dispose).not.toHaveBeenCalled()

    const pendingTab = screen.getByRole('tab', { name: 'SlowServer' })
    await user.click(pendingTab)
    expect(pendingTab).toHaveFocus()

    // Now resolve the connection
    await act(async () => {
      resolveSlowConnect({ sessionId: 's2' })
    })

    const slowTab = await screen.findByRole('tab', { name: /SlowServer/ })
    expect(slowTab).toHaveAttribute('aria-selected', 'true')
    expect(slowTab).not.toBe(pendingTab)
    expect(slowTab).toHaveFocus()
    expect(mockInstances.length).toBe(2)
    expect(termA.dispose).not.toHaveBeenCalled()
  })

  it('disposes only the closed session terminal instance and preserves remaining sessions', async () => {
    const fake = await renderApp([
      makeHost({ id: 'h1', name: 'Alpha', host: '10.0.0.1' }),
      makeHost({ id: 'h2', name: 'Beta', host: '10.0.0.2' })
    ])
    fake.mocks.sessions.connect.mockImplementation((hostId: string) =>
      Promise.resolve({ sessionId: hostId === 'h1' ? 's1' : 's2' })
    )
    const user = userEvent.setup()

    await openHostsPicker(user)
    await user.click(within(hostRow('Alpha')).getByRole('button', { name: 'Connect' }))
    const alphaTab = await screen.findByRole('tab', { name: /Alpha/ })

    await openHostsPicker(user)
    await user.click(within(hostRow('Beta')).getByRole('button', { name: 'Connect' }))
    await screen.findByRole('tab', { name: /Beta/ })

    expect(mockInstances.length).toBe(2)
    const termA = mockInstances[0]!
    const termB = mockInstances[1]!

    // Close Beta
    await user.click(screen.getByRole('button', { name: 'Close Beta' }))
    await waitFor(() => expect(fake.mocks.sessions.disconnect).toHaveBeenCalledWith('s2'))

    // termB must be disposed exactly once
    expect(termB.dispose).toHaveBeenCalledTimes(1)
    // termA must NOT be disposed
    expect(termA.dispose).not.toHaveBeenCalled()

    // Alpha tab is active again
    expect(alphaTab).toHaveAttribute('aria-selected', 'true')
    expect(screen.queryByRole('tab', { name: /Beta/ })).not.toBeInTheDocument()

    // Close Alpha
    await user.click(screen.getByRole('button', { name: 'Close Alpha' }))
    await waitFor(() => expect(fake.mocks.sessions.disconnect).toHaveBeenCalledWith('s1'))

    // termA is now disposed
    expect(termA.dispose).toHaveBeenCalledTimes(1)
  })

  it('routes simultaneous concurrent output to Alpha and Beta during rapid tab switching without crosstalk', async () => {
    const fake = await renderApp([
      makeHost({ id: 'h1', name: 'Alpha', host: '10.0.0.1' }),
      makeHost({ id: 'h2', name: 'Beta', host: '10.0.0.2' })
    ])
    fake.mocks.sessions.connect.mockImplementation((hostId: string) =>
      Promise.resolve({ sessionId: hostId === 'h1' ? 's1' : 's2' })
    )
    const user = userEvent.setup()

    await openHostsPicker(user)
    await user.click(within(hostRow('Alpha')).getByRole('button', { name: 'Connect' }))
    const alphaTab = await screen.findByRole('tab', { name: /Alpha/ })

    await openHostsPicker(user)
    await user.click(within(hostRow('Beta')).getByRole('button', { name: 'Connect' }))
    const betaTab = await screen.findByRole('tab', { name: /Beta/ })

    expect(mockInstances.length).toBe(2)
    const termA = mockInstances[0]!
    const termB = mockInstances[1]!

    // Concurrently emit interleaved bursts while rapidly switching tabs
    for (let i = 0; i < 10; i++) {
      act(() => {
        emitSessionEvent(fake, 'data', { sessionId: 's1', data: `alpha-burst-${i}\n` })
        emitSessionEvent(fake, 'data', { sessionId: 's2', data: `beta-burst-${i}\n` })
      })
      if (i % 2 === 0) {
        await user.click(alphaTab)
      } else {
        await user.click(betaTab)
      }
    }

    // Verify termA received alpha data and never received beta data
    await waitFor(() => {
      expect(termA.write).toHaveBeenCalledWith(expect.stringContaining('alpha-burst-9'))
    })
    expect(termA.write).not.toHaveBeenCalledWith(expect.stringContaining('beta-burst'))

    // Verify termB received beta data and never received alpha data
    await waitFor(() => {
      expect(termB.write).toHaveBeenCalledWith(expect.stringContaining('beta-burst-9'))
    })
    expect(termB.write).not.toHaveBeenCalledWith(expect.stringContaining('alpha-burst'))

    // Both terminals stayed alive without unnecessary re-creations
    expect(mockInstances.length).toBe(2)
    expect(termA.dispose).not.toHaveBeenCalled()
    expect(termB.dispose).not.toHaveBeenCalled()
  })

  it('reconnects after a remote close by replacing only that terminal instance', async () => {
    const fake = await renderApp([makeHost({ id: 'h1', name: 'Alpha', host: '10.0.0.1' })])
    fake.mocks.sessions.connect
      .mockResolvedValueOnce({ sessionId: 's1' })
      .mockResolvedValueOnce({ sessionId: 's1-re' })
    const user = userEvent.setup()

    await openHostsPicker(user)
    await user.click(within(hostRow('Alpha')).getByRole('button', { name: 'Connect' }))
    await screen.findByRole('tab', { name: /Alpha/ })
    await waitFor(() => expect(mockInstances.length).toBe(1))
    const termA = mockInstances[0]!

    act(() => {
      emitSessionEvent(fake, 'closed', { sessionId: 's1' })
    })
    await user.click(await screen.findByRole('button', { name: 'Reconnect' }))

    await waitFor(() => expect(fake.mocks.sessions.connect).toHaveBeenCalledTimes(2))
    await waitFor(() => expect(termA.dispose).toHaveBeenCalledTimes(1))
    await waitFor(() => expect(mockInstances.length).toBe(2))
    expect(mockInstances[1]!.dispose).not.toHaveBeenCalled()
  })

  it('treats a repeated remote close as idempotent and does not dispose the waiting tab', async () => {
    const fake = await renderApp([makeHost({ id: 'h1', name: 'Alpha', host: '10.0.0.1' })])
    fake.mocks.sessions.connect.mockResolvedValue({ sessionId: 's1' })
    const user = userEvent.setup()

    await openHostsPicker(user)
    await user.click(within(hostRow('Alpha')).getByRole('button', { name: 'Connect' }))
    await screen.findByRole('tab', { name: /Alpha/ })
    await waitFor(() => expect(mockInstances.length).toBe(1))
    const termA = mockInstances[0]!

    act(() => {
      emitSessionEvent(fake, 'closed', { sessionId: 's1' })
      emitSessionEvent(fake, 'closed', { sessionId: 's1' })
    })

    expect(await screen.findByRole('button', { name: 'Reconnect' })).toBeInTheDocument()
    expect(termA.dispose).not.toHaveBeenCalled()
    expect(mockInstances.length).toBe(1)
  })
})

// @vitest-environment jsdom
import { describe, expect, it, vi } from 'vitest'
import { act, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import App from '../../src/renderer/src/App'
import { emitSessionEvent, installFakeApi, renderWithI18n, type FakeApi } from './helpers'
import type { AppSettings, HostConfig } from '../../src/shared/types'

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

describe('Session Terminal Persistence', () => {
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

    // Now resolve the connection
    await act(async () => {
      resolveSlowConnect({ sessionId: 's2' })
    })

    const slowTab = await screen.findByRole('tab', { name: /SlowServer/ })
    expect(slowTab).toHaveAttribute('aria-selected', 'true')
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

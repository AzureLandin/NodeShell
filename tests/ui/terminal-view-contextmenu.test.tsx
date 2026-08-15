// @vitest-environment jsdom
import { describe, expect, it, vi } from 'vitest'
import { fireEvent, screen, waitFor } from '@testing-library/react'
import { TerminalView } from '../../src/renderer/src/components/TerminalView'
import { installFakeApi, renderWithI18n } from './helpers'

vi.mock('@xterm/xterm', () => {
  const instances: MockTerminal[] = []
  class MockTerminal {
    cols = 80
    rows = 24
    options: Record<string, unknown>
    selection = ''
    loadAddon = vi.fn()
    open = vi.fn()
    write = vi.fn()
    dispose = vi.fn()
    onData = vi.fn(() => ({ dispose: vi.fn() }))
    getSelection = vi.fn(() => this.selection)
    clear = vi.fn()
    clearSelection = vi.fn()
    paste = vi.fn()
    constructor(options: Record<string, unknown> = {}) {
      this.options = options
      instances.push(this)
    }
  }
  return { Terminal: MockTerminal, mockTerminalInstances: instances }
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

type MockTerminal = {
  options: Record<string, unknown>
  selection: string
  getSelection: ReturnType<typeof vi.fn>
  clear: ReturnType<typeof vi.fn>
  clearSelection: ReturnType<typeof vi.fn>
  paste: ReturnType<typeof vi.fn>
  loadAddon: ReturnType<typeof vi.fn>
  open: ReturnType<typeof vi.fn>
}

/** The terminal instance created by the last TerminalView render. */
async function latestTerminal(): Promise<MockTerminal> {
  const mod = (await import('@xterm/xterm')) as unknown as {
    mockTerminalInstances: MockTerminal[]
  }
  const inst = mod.mockTerminalInstances[mod.mockTerminalInstances.length - 1]
  if (!inst) throw new Error('no terminal instance created')
  return inst
}

describe('TerminalView context menu', () => {
  function renderView(): { container: HTMLElement } {
    installFakeApi()
    const { container } = renderWithI18n(
      <TerminalView
        sessionId="s1"
        registerDataListener={() => () => undefined}
        visible={true}
        fontFamily="Hack"
        fontSize={14}
        resolvedTheme="dark"
      />
    )
    return { container }
  }

  it('loads grapheme width handling and the WebGL renderer after open', async () => {
    renderView()
    const term = await latestTerminal()
    expect(term.options.allowProposedApi).toBe(true)
    expect(term.open).toHaveBeenCalled()
    const names = term.loadAddon.mock.calls.map(
      (call: unknown[]) => (call[0] as { constructor: { name: string } }).constructor.name
    )
    expect(names).toContain('MockFitAddon')
    expect(names).toContain('MockUnicodeGraphemesAddon')
    expect(names).toContain('MockWebglAddon')
    const openOrder = term.open.mock.invocationCallOrder[0]
    const webglCall = term.loadAddon.mock.calls.findIndex(
      (call: unknown[]) =>
        (call[0] as { constructor: { name: string } }).constructor.name === 'MockWebglAddon'
    )
    expect(term.loadAddon.mock.invocationCallOrder[webglCall]).toBeGreaterThan(openOrder)
  })

  it('opens the menu on right-click with the terminal selection state', async () => {
    const { container } = renderView()
    const term = await latestTerminal()
    term.getSelection.mockReturnValue('selected text')
    fireEvent.contextMenu(container.firstElementChild as HTMLElement, {
      clientX: 100,
      clientY: 120
    })
    expect(await screen.findByRole('menu')).toBeInTheDocument()
    expect(screen.getByRole('menuitem', { name: 'Copy' })).toBeEnabled()
  })

  it('Copy writes the selection to the clipboard', async () => {
    const { container } = renderView()
    const term = await latestTerminal()
    term.getSelection.mockReturnValue('hello')
    const writeText = vi.fn().mockResolvedValue(undefined)
    Object.defineProperty(navigator, 'clipboard', {
      value: { writeText, readText: vi.fn().mockResolvedValue('') },
      configurable: true
    })
    fireEvent.contextMenu(container.firstElementChild as HTMLElement, {
      clientX: 100,
      clientY: 120
    })
    await screen.findByRole('menu')
    fireEvent.click(screen.getByRole('menuitem', { name: 'Copy' }))
    await waitFor(() => expect(writeText).toHaveBeenCalledWith('hello'))
    expect(term.clearSelection).toHaveBeenCalled()
  })

  it('Paste reads the clipboard and sends it to the terminal', async () => {
    const { container } = renderView()
    const term = await latestTerminal()
    const readText = vi.fn().mockResolvedValue('pasted text')
    Object.defineProperty(navigator, 'clipboard', {
      value: { writeText: vi.fn(), readText },
      configurable: true
    })
    fireEvent.contextMenu(container.firstElementChild as HTMLElement, {
      clientX: 100,
      clientY: 120
    })
    await screen.findByRole('menu')
    fireEvent.click(screen.getByRole('menuitem', { name: 'Paste' }))
    await waitFor(() => expect(readText).toHaveBeenCalled())
    await waitFor(() => expect(term.paste).toHaveBeenCalledWith('pasted text'))
  })

  it('Clear Screen clears the terminal viewport', async () => {
    const { container } = renderView()
    const term = await latestTerminal()
    fireEvent.contextMenu(container.firstElementChild as HTMLElement, {
      clientX: 100,
      clientY: 120
    })
    await screen.findByRole('menu')
    fireEvent.click(screen.getByRole('menuitem', { name: 'Clear Screen' }))
    expect(term.clear).toHaveBeenCalled()
    expect(term.clearSelection).toHaveBeenCalled()
  })

  it('closes the menu on outside click', async () => {
    const { container } = renderView()
    fireEvent.contextMenu(container.firstElementChild as HTMLElement, {
      clientX: 100,
      clientY: 120
    })
    await screen.findByRole('menu')
    fireEvent.mouseDown(document.body)
    expect(screen.queryByRole('menu')).not.toBeInTheDocument()
  })
})

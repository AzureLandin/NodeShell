// @vitest-environment jsdom
import { describe, expect, it, vi } from 'vitest'
import { fireEvent, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { TerminalView } from '../../src/renderer/src/features/sessions/TerminalView'
import { installFakeApi, renderWithI18n } from '../support/helpers'

vi.mock('@xterm/xterm', () => {
  const instances: MockTerminal[] = []
  class MockTerminal {
    cols = 80
    rows = 24
    options: Record<string, unknown>
    selection = ''
    textarea = document.createElement('textarea')
    keyHandler: ((event: KeyboardEvent) => boolean) | undefined
    dataHandler: ((data: string) => void) | undefined
    loadAddon = vi.fn()
    open = vi.fn((container: HTMLElement) => {
      container.append(this.textarea)
      this.textarea.addEventListener('keydown', (event) => {
        if (this.keyHandler?.(event) === false) return
        if (event.key === 'Escape') this.dataHandler?.('\u001b')
        else if (event.key.length === 1) this.dataHandler?.(event.key)
      })
    })
    write = vi.fn()
    dispose = vi.fn()
    onData = vi.fn((handler: (data: string) => void) => {
      this.dataHandler = handler
      return { dispose: vi.fn() }
    })
    attachCustomKeyEventHandler = vi.fn((handler: (event: KeyboardEvent) => boolean) => {
      this.keyHandler = handler
    })
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
  textarea: HTMLTextAreaElement
  keyHandler: (event: KeyboardEvent) => boolean
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
  const registerDataListener = (): (() => void) => () => undefined
  function view(visible = true): React.JSX.Element {
    return (
      <TerminalView
        sessionId="s1"
        registerDataListener={registerDataListener}
        visible={visible}
        fontFamily="Hack"
        fontSize={14}
        resolvedTheme="dark"
      />
    )
  }
  function renderView(): ReturnType<typeof renderWithI18n> & {
    fake: ReturnType<typeof installFakeApi>
  } {
    const fake = installFakeApi()
    return { ...renderWithI18n(view()), fake }
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
    await userEvent.click(document.body)
    await waitFor(() => expect(screen.queryByRole('menu')).not.toBeInTheDocument())
  })

  it('blocks remote input while open, then restores terminal focus and ordinary Escape', async () => {
    const user = userEvent.setup()
    const { container, fake } = renderView()
    const term = await latestTerminal()
    term.textarea.focus()
    fireEvent.contextMenu(container.firstElementChild as HTMLElement)
    await screen.findByRole('menu')
    // xterm may still own focus when the native contextmenu event arrives.
    fireEvent.keyDown(term.textarea, { key: 'x' })
    expect(term.keyHandler(new KeyboardEvent('keydown', { key: 'ArrowDown' }))).toBe(false)
    expect(fake.mocks.sessions.write).not.toHaveBeenCalled()
    await user.keyboard('{ArrowDown}{Escape}')
    await waitFor(() => expect(screen.queryByRole('menu')).not.toBeInTheDocument())
    await waitFor(() => expect(term.textarea).toHaveFocus())
    expect(fake.mocks.sessions.write).not.toHaveBeenCalled()
    await user.keyboard('{Escape}x')
    expect(fake.mocks.sessions.write).toHaveBeenNthCalledWith(1, 's1', '\u001b')
    expect(fake.mocks.sessions.write).toHaveBeenNthCalledWith(2, 's1', 'x')
  })

  it('updates the selection when right-clicking again without creating another menu', async () => {
    const { container } = renderView()
    const term = await latestTerminal()
    fireEvent.contextMenu(container.firstElementChild as HTMLElement, { clientX: 10, clientY: 20 })
    expect(screen.getByRole('menuitem', { name: 'Copy' })).toHaveAttribute('aria-disabled', 'true')
    term.getSelection.mockReturnValue('new selection')
    fireEvent.contextMenu(container.firstElementChild as HTMLElement, {
      clientX: 100,
      clientY: 120
    })
    expect(screen.getAllByRole('menu')).toHaveLength(1)
    expect(screen.getByRole('menuitem', { name: 'Copy' })).toHaveAttribute('aria-disabled', 'false')
  })

  it('closes when hidden and releases the portal on unmount', async () => {
    const { container, rerender, unmount } = renderView()
    fireEvent.contextMenu(container.firstElementChild as HTMLElement)
    await screen.findByRole('menu')
    rerender(view(false))
    await waitFor(() => expect(screen.queryByRole('menu')).not.toBeInTheDocument())
    rerender(view())
    fireEvent.contextMenu(container.firstElementChild as HTMLElement)
    await screen.findByRole('menu')
    unmount()
    expect(screen.queryByRole('menu')).not.toBeInTheDocument()
  })
})

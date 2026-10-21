// @vitest-environment jsdom
import { describe, expect, it, vi } from 'vitest'
import { fireEvent, screen } from '@testing-library/react'
import { TerminalContextMenu } from '../../src/renderer/src/components/TerminalContextMenu'
import { renderWithI18n } from './helpers'

interface MenuHarness {
  onCopy: ReturnType<typeof vi.fn>
  onPaste: ReturnType<typeof vi.fn>
  onClear: ReturnType<typeof vi.fn>
  onClose: ReturnType<typeof vi.fn>
  unmount: () => void
}

/** Render the menu with fresh spies; the menu closes itself after an item. */
function renderMenu(canCopy = true): MenuHarness {
  const onCopy = vi.fn()
  const onPaste = vi.fn()
  const onClear = vi.fn()
  const onClose = vi.fn()
  const view = renderWithI18n(
    <TerminalContextMenu
      x={10}
      y={20}
      canCopy={canCopy}
      onCopy={onCopy}
      onPaste={onPaste}
      onClear={onClear}
      onClose={onClose}
    />
  )
  return { onCopy, onPaste, onClear, onClose, unmount: view.unmount }
}

describe('TerminalContextMenu', () => {
  it('renders Copy, Paste, and Clear Screen items', () => {
    renderWithI18n(
      <TerminalContextMenu
        x={10}
        y={20}
        canCopy={true}
        onCopy={vi.fn()}
        onPaste={vi.fn()}
        onClear={vi.fn()}
        onClose={vi.fn()}
      />
    )
    expect(screen.getByRole('menu')).toBeInTheDocument()
    expect(screen.getByRole('menuitem', { name: 'Copy' })).toBeInTheDocument()
    expect(screen.getByRole('menuitem', { name: 'Paste' })).toBeInTheDocument()
    expect(screen.getByRole('menuitem', { name: 'Clear Screen' })).toBeInTheDocument()
  })

  it('separates clipboard actions from Clear Screen with a divider', () => {
    renderWithI18n(
      <TerminalContextMenu
        x={0}
        y={0}
        canCopy={true}
        onCopy={vi.fn()}
        onPaste={vi.fn()}
        onClear={vi.fn()}
        onClose={vi.fn()}
      />
    )
    const menu = screen.getByRole('menu')
    const items = Array.from(menu.children)
    const dividerIndex = items.findIndex((el) => el.getAttribute('role') === 'separator')
    expect(dividerIndex).toBeGreaterThan(-1)
    // Divider sits between Paste (clipboard group) and Clear Screen.
    expect(items[dividerIndex - 1]).toHaveTextContent('Paste')
    expect(items[dividerIndex + 1]).toHaveTextContent('Clear Screen')
  })

  it('disables Copy when there is no selection', () => {
    renderMenu(false)
    // Base UI marks disabled menu items with aria-disabled so they stay
    // focusable for keyboard navigation.
    const copy = screen.getByRole('menuitem', { name: 'Copy' })
    expect(copy).toHaveAttribute('aria-disabled', 'true')
    expect(copy).toHaveAttribute('data-disabled')
    expect(screen.getByRole('menuitem', { name: 'Paste' })).toHaveAttribute(
      'aria-disabled',
      'false'
    )
  })

  it('does not run the action of a disabled item', () => {
    const harness = renderMenu(false)
    fireEvent.click(screen.getByRole('menuitem', { name: 'Copy' }))
    expect(harness.onCopy).not.toHaveBeenCalled()
  })

  it('invokes the action callbacks and closes the menu on select', () => {
    // Base UI closes the menu when an item is activated, so every action is
    // exercised in its own render.
    const copy = renderMenu()
    fireEvent.click(screen.getByRole('menuitem', { name: 'Copy' }))
    expect(copy.onCopy).toHaveBeenCalledTimes(1)
    expect(copy.onClose).toHaveBeenCalledTimes(1)
    expect(screen.queryByRole('menu')).not.toBeInTheDocument()
    copy.unmount()

    const paste = renderMenu()
    fireEvent.click(screen.getByRole('menuitem', { name: 'Paste' }))
    expect(paste.onPaste).toHaveBeenCalledTimes(1)
    expect(paste.onClose).toHaveBeenCalledTimes(1)
    paste.unmount()

    const clear = renderMenu()
    fireEvent.click(screen.getByRole('menuitem', { name: 'Clear Screen' }))
    expect(clear.onClear).toHaveBeenCalledTimes(1)
    expect(clear.onClose).toHaveBeenCalledTimes(1)
    clear.unmount()
  })
})

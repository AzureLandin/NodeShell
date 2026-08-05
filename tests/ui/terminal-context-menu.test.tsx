// @vitest-environment jsdom
import { describe, expect, it, vi } from 'vitest'
import { fireEvent, screen } from '@testing-library/react'
import { TerminalContextMenu } from '../../src/renderer/src/components/TerminalContextMenu'
import { renderWithI18n } from './helpers'

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
    const dividerIndex = items.findIndex(
      (el) => el.getAttribute('role') === 'separator'
    )
    expect(dividerIndex).toBeGreaterThan(-1)
    // Divider sits between Paste (clipboard group) and Clear Screen.
    expect(items[dividerIndex - 1]).toHaveTextContent('Paste')
    expect(items[dividerIndex + 1]).toHaveTextContent('Clear Screen')
  })

  it('disables Copy when there is no selection', () => {
    renderWithI18n(
      <TerminalContextMenu
        x={0}
        y={0}
        canCopy={false}
        onCopy={vi.fn()}
        onPaste={vi.fn()}
        onClear={vi.fn()}
        onClose={vi.fn()}
      />
    )
    expect(screen.getByRole('menuitem', { name: 'Copy' })).toBeDisabled()
  })

  it('invokes the action callbacks', () => {
    const onCopy = vi.fn()
    const onPaste = vi.fn()
    const onClear = vi.fn()
    const onClose = vi.fn()
    renderWithI18n(
      <TerminalContextMenu
        x={0}
        y={0}
        canCopy={true}
        onCopy={onCopy}
        onPaste={onPaste}
        onClear={onClear}
        onClose={onClose}
      />
    )
    fireEvent.click(screen.getByRole('menuitem', { name: 'Copy' }))
    fireEvent.click(screen.getByRole('menuitem', { name: 'Paste' }))
    fireEvent.click(screen.getByRole('menuitem', { name: 'Clear Screen' }))
    expect(onCopy).toHaveBeenCalledTimes(1)
    expect(onPaste).toHaveBeenCalledTimes(1)
    expect(onClear).toHaveBeenCalledTimes(1)
    expect(onClose).toHaveBeenCalledTimes(3)
  })
})

// @vitest-environment jsdom
import { describe, expect, it, vi } from 'vitest'
import { fireEvent, render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { ModalShell } from '../../src/renderer/src/components/ModalShell'

describe('ModalShell motion isolation', () => {
  it('applies default animated classes when motion is default', () => {
    const onClose = vi.fn()
    render(
      <ModalShell onClose={onClose} motion="default" dialogClassName="test-modal">
        <div>Content</div>
      </ModalShell>
    )

    const dialog = screen.getByRole('dialog')
    expect(dialog).toHaveClass('modal--animated')
    expect(dialog).not.toHaveClass('modal-motion-simple')
    expect(dialog).toHaveClass('test-modal')

    const overlay = dialog.parentElement
    expect(overlay).toHaveClass('modal-overlay--animated')
    expect(overlay).not.toHaveClass('modal-overlay--simple')
  })

  it('applies simple motion classes without scale animation when motion is simple', () => {
    const onClose = vi.fn()
    render(
      <ModalShell onClose={onClose} motion="simple" dialogClassName="settings-modal">
        <div>Settings Content</div>
      </ModalShell>
    )

    const dialog = screen.getByRole('dialog')
    expect(dialog).toHaveClass('modal-motion-simple')
    expect(dialog).not.toHaveClass('modal--animated')
    expect(dialog).toHaveClass('settings-modal')

    const overlay = dialog.parentElement
    expect(overlay).toHaveClass('modal-overlay--simple')
    expect(overlay).not.toHaveClass('modal-overlay--animated')
  })

  it('omits all animated classes when motion is none', () => {
    const onClose = vi.fn()
    render(
      <ModalShell onClose={onClose} motion="none" dialogClassName="plain-modal">
        <div>Plain Content</div>
      </ModalShell>
    )

    const dialog = screen.getByRole('dialog')
    expect(dialog).not.toHaveClass('modal--animated')
    expect(dialog).not.toHaveClass('modal-motion-simple')
    expect(dialog).toHaveClass('plain-modal')

    const overlay = dialog.parentElement
    expect(overlay).not.toHaveClass('modal-overlay--animated')
    expect(overlay).not.toHaveClass('modal-overlay--simple')
  })

  it('closes on Escape key press in simple motion mode', async () => {
    const onClose = vi.fn()
    render(
      <ModalShell onClose={onClose} motion="simple">
        <div>Content</div>
      </ModalShell>
    )

    fireEvent.keyDown(window, { key: 'Escape' })
    expect(onClose).toHaveBeenCalledTimes(1)
  })

  it('closes on overlay backdrop click in simple motion mode', async () => {
    const onClose = vi.fn()
    render(
      <ModalShell onClose={onClose} motion="simple">
        <div>Content</div>
      </ModalShell>
    )

    const dialog = screen.getByRole('dialog')
    const overlay = dialog.parentElement as HTMLElement

    // Pointer down and click on overlay
    fireEvent.pointerDown(overlay)
    fireEvent.click(overlay)
    expect(onClose).toHaveBeenCalledTimes(1)
  })

  it('does not close when clicking inside dialog content', async () => {
    const onClose = vi.fn()
    render(
      <ModalShell onClose={onClose} motion="simple">
        <button type="button">Inside Button</button>
      </ModalShell>
    )

    const button = screen.getByRole('button', { name: 'Inside Button' })
    const user = userEvent.setup()
    await user.click(button)
    expect(onClose).not.toHaveBeenCalled()
  })
})

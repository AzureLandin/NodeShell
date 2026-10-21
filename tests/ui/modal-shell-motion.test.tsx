// @vitest-environment jsdom
import { describe, expect, it, vi } from 'vitest'
import { fireEvent, render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { ModalShell } from '../../src/renderer/src/components/ModalShell'

function overlayOf(dialog: HTMLElement): HTMLElement {
  const overlay = dialog.closest('.modal-overlay')
  if (!(overlay instanceof HTMLElement)) throw new Error('modal overlay not found')
  return overlay
}

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

    const overlay = overlayOf(dialog)
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

    const overlay = overlayOf(dialog)
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

    const overlay = overlayOf(dialog)
    expect(overlay).not.toHaveClass('modal-overlay--animated')
    expect(overlay).not.toHaveClass('modal-overlay--simple')
  })

  it('marks the sheet as an open modal once mounted', () => {
    const onClose = vi.fn()
    render(
      <ModalShell onClose={onClose} labelledBy="shell-title">
        <h3 id="shell-title">Shell title</h3>
      </ModalShell>
    )

    const dialog = screen.getByRole('dialog', { name: 'Shell title' })
    expect(dialog).toHaveAttribute('aria-modal', 'true')
    expect(dialog).toHaveAttribute('aria-labelledby', 'shell-title')
    expect(dialog).toHaveClass('is-open')
    expect(overlayOf(dialog)).toHaveClass('is-open')
  })

  it('closes on Escape key press in simple motion mode', async () => {
    const onClose = vi.fn()
    render(
      <ModalShell onClose={onClose} motion="simple">
        <div>Content</div>
      </ModalShell>
    )

    fireEvent.keyDown(document, { key: 'Escape' })
    await waitFor(() => expect(onClose).toHaveBeenCalledTimes(1))
  })

  it('keeps the modal open on Escape when closeOnEscape is false', async () => {
    const onClose = vi.fn()
    render(
      <ModalShell onClose={onClose} motion="simple" closeOnEscape={false}>
        <div>Content</div>
      </ModalShell>
    )

    fireEvent.keyDown(document, { key: 'Escape' })
    await waitFor(() => expect(screen.getByRole('dialog')).toBeInTheDocument())
    expect(onClose).not.toHaveBeenCalled()
  })

  it('closes on overlay backdrop click in simple motion mode', async () => {
    const onClose = vi.fn()
    render(
      <ModalShell onClose={onClose} motion="simple">
        <div>Content</div>
      </ModalShell>
    )

    const dialog = screen.getByRole('dialog')
    const overlay = overlayOf(dialog)

    // Pointer down and click on overlay
    fireEvent.pointerDown(overlay)
    fireEvent.click(overlay)
    await waitFor(() => expect(onClose).toHaveBeenCalledTimes(1))
  })

  it('keeps the modal open on backdrop click when closeOnOverlayClick is false', async () => {
    const onClose = vi.fn()
    render(
      <ModalShell onClose={onClose} motion="simple" closeOnOverlayClick={false}>
        <div>Content</div>
      </ModalShell>
    )

    const dialog = screen.getByRole('dialog')
    const overlay = overlayOf(dialog)

    fireEvent.pointerDown(overlay)
    fireEvent.click(overlay)
    await waitFor(() => expect(screen.getByRole('dialog')).toBeInTheDocument())
    expect(onClose).not.toHaveBeenCalled()
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

  it('moves focus into the dialog and back to the opener on close', async () => {
    const onClose = vi.fn()
    const outside = document.createElement('button')
    outside.textContent = 'Outside'
    document.body.appendChild(outside)
    outside.focus()

    render(
      <ModalShell onClose={onClose} motion="simple">
        <button type="button">Inside Button</button>
      </ModalShell>
    )

    await waitFor(() => expect(screen.getByRole('button', { name: 'Inside Button' })).toHaveFocus())

    fireEvent.keyDown(document, { key: 'Escape' })
    await waitFor(() => expect(onClose).toHaveBeenCalledTimes(1))
    await waitFor(() => expect(outside).toHaveFocus())

    outside.remove()
  })
})

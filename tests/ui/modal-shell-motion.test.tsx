// @vitest-environment jsdom
import { describe, expect, it, vi } from 'vitest'
import { act, fireEvent, render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { ModalShell, useModalClose } from '../../src/renderer/src/ui/ModalShell'

function CloseTwiceButton(): React.JSX.Element {
  const requestClose = useModalClose()
  return (
    <button
      type="button"
      onClick={() => {
        requestClose()
        requestClose()
      }}
    >
      Request close twice
    </button>
  )
}

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

  it('opens without a staged enter transition when reduced motion is preferred', () => {
    const onClose = vi.fn()
    const matchMedia = vi.spyOn(window, 'matchMedia')
    render(
      <ModalShell onClose={onClose} labelledBy="shell-title">
        <h3 id="shell-title">Shell title</h3>
      </ModalShell>
    )

    const dialog = screen.getByRole('dialog', { name: 'Shell title' })
    expect(dialog).toHaveAttribute('aria-modal', 'true')
    expect(dialog).toHaveAttribute('aria-labelledby', 'shell-title')
    expect(dialog).toHaveClass('is-open')
    expect(dialog).not.toHaveAttribute('data-starting-style')
    expect(overlayOf(dialog)).toHaveClass('is-open')
    expect(matchMedia).toHaveBeenCalledWith('(prefers-reduced-motion: reduce)')
    matchMedia.mockRestore()
  })

  it('notifies the owner once after Base UI completes the exit animation', async () => {
    const onClose = vi.fn()
    const user = userEvent.setup()
    let finishExit: (() => void) | undefined
    const exitFinished = new Promise<void>((resolve) => {
      finishExit = resolve
    })
    const exitAnimation = {
      effect: { getTiming: () => ({ duration: 120, iterations: 1 }) },
      finished: exitFinished,
      pending: true,
      playState: 'running'
    } as unknown as Animation

    render(
      <ModalShell onClose={onClose}>
        <CloseTwiceButton />
      </ModalShell>
    )

    const dialog = screen.getByRole('dialog')
    Object.defineProperty(dialog, 'getAnimations', {
      configurable: true,
      value: () => [exitAnimation]
    })

    await user.click(screen.getByRole('button', { name: 'Request close twice' }))

    expect(dialog).toHaveClass('is-closing')
    expect(onClose).not.toHaveBeenCalled()
    expect(screen.getByRole('dialog')).toBeInTheDocument()

    await act(async () => {
      finishExit?.()
      await exitFinished
    })

    await waitFor(() => expect(onClose).toHaveBeenCalledTimes(1))
    expect(screen.queryByRole('dialog')).not.toBeInTheDocument()
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

  it('allows a nested Escape handler to receive Escape when shell dismissal is disabled', () => {
    const onClose = vi.fn()
    const windowEscape = vi.fn()
    const onWindowKeyDown = (event: KeyboardEvent): void => {
      if (event.key === 'Escape') windowEscape()
    }
    window.addEventListener('keydown', onWindowKeyDown)
    const { unmount } = render(
      <ModalShell onClose={onClose} closeOnEscape={false}>
        <div>Nested form</div>
      </ModalShell>
    )

    fireEvent.keyDown(screen.getByRole('dialog'), { key: 'Escape' })

    expect(windowEscape).toHaveBeenCalledTimes(1)
    expect(onClose).not.toHaveBeenCalled()
    expect(screen.getByRole('dialog')).toBeInTheDocument()

    unmount()
    window.removeEventListener('keydown', onWindowKeyDown)
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

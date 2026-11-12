// @vitest-environment jsdom
import { render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import type { FormEvent } from 'react'
import { describe, expect, it, vi } from 'vitest'
import { Button } from '../../src/renderer/src/ui/button'
import { IconButton } from '../../src/renderer/src/ui/icon-button'

describe('shared buttons', () => {
  it('defaults to a non-submit button and applies the chosen visual variant', async () => {
    const onSubmit = vi.fn((event: FormEvent<HTMLFormElement>) => event.preventDefault())
    const user = userEvent.setup()
    render(
      <form onSubmit={onSubmit}>
        <Button variant="primary" size="small">
          Save
        </Button>
        <Button type="submit">Submit</Button>
      </form>
    )

    const button = screen.getByRole('button', { name: 'Save' })
    expect(button).toHaveAttribute('type', 'button')
    expect(button).toHaveClass('btn-primary', 'btn-sm')

    await user.click(button)
    expect(onSubmit).not.toHaveBeenCalled()

    await user.click(screen.getByRole('button', { name: 'Submit' }))
    expect(onSubmit).toHaveBeenCalledTimes(1)
  })

  it('disables loading actions, exposes their busy state, and blocks clicks', async () => {
    const onClick = vi.fn()
    const user = userEvent.setup()
    render(
      <Button variant="danger" loading onClick={onClick}>
        Deleting
      </Button>
    )

    const button = screen.getByRole('button', { name: 'Deleting' })
    expect(button).toBeDisabled()
    expect(button).toHaveAttribute('aria-busy', 'true')
    expect(button).toHaveClass('btn-danger')

    await user.click(button)
    expect(onClick).not.toHaveBeenCalled()
  })

  it('merges callback class names with variant classes', () => {
    render(
      <Button
        variant="secondary"
        disabled
        className={(state) => (state.disabled ? 'state-disabled' : 'state-enabled')}
      >
        Disabled
      </Button>
    )

    expect(screen.getByRole('button', { name: 'Disabled' })).toHaveClass(
      'btn-secondary',
      'state-disabled'
    )
  })

  it('requires an accessible name for icon-only actions', () => {
    render(<IconButton aria-label="Close dialog">x</IconButton>)

    const button = screen.getByRole('button', { name: 'Close dialog' })
    expect(button.querySelector('[aria-hidden="true"]')).toHaveTextContent('x')
  })
})

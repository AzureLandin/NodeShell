// @vitest-environment jsdom
import { describe, expect, it } from 'vitest'
import { render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { FormField } from '../../src/renderer/src/ui/field'
import { Input } from '../../src/renderer/src/ui/input'
import { Select } from '../../src/renderer/src/ui/select'
import { FormNumberField } from '../../src/renderer/src/ui/number-field'

describe('shared field associations', () => {
  it('associates text labels, help and external errors, clearing the error without losing help', async () => {
    const user = userEvent.setup()
    const field = (error?: string): React.JSX.Element => (
      <FormField label="Address" description="Use a hostname" error={error}>
        <Input />
      </FormField>
    )
    const view = render(field('Address is required'))
    const input = screen.getByLabelText('Address')
    expect(input).toHaveAccessibleDescription('Use a hostname Address is required')
    expect(input).toHaveAttribute('aria-invalid', 'true')
    await user.click(screen.getByText('Address'))
    expect(input).toHaveFocus()
    view.rerender(field())
    expect(input).toHaveAccessibleDescription('Use a hostname')
    expect(input).not.toHaveAttribute('aria-invalid', 'true')
    expect(screen.queryByRole('alert')).not.toBeInTheDocument()
  })

  it('associates Select and NumberField controls with their own help and errors', () => {
    render(
      <>
        <FormField label="Region" description="Choose a region" error="Region is unavailable">
          <Select value="us" onChange={() => undefined} options={[{ value: 'us', label: 'US' }]} />
        </FormField>
        <FormField label="Count" description="Use whole numbers" error="Count is too large">
          <FormNumberField value={2} />
        </FormField>
      </>
    )
    expect(screen.getByLabelText('Region')).toHaveAccessibleDescription(
      'Choose a region Region is unavailable'
    )
    expect(screen.getByLabelText('Region')).toHaveAttribute('aria-invalid', 'true')
    expect(screen.getByLabelText('Count')).toHaveAccessibleDescription(
      'Use whole numbers Count is too large'
    )
    expect(screen.getByLabelText('Count')).toHaveAttribute('aria-invalid', 'true')
  })
})

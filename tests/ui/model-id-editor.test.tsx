// @vitest-environment jsdom
import { describe, expect, it, vi } from 'vitest'
import { screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { useState } from 'react'
import { renderWithI18n } from './helpers'
import { ModelIdEditor } from '../../src/renderer/src/components/ModelIdEditor'

function ControlledEditor({
  initial = [],
  disabled = false,
  maxItems = 32,
  maxItemLength = 512,
  onChangeSpy
}: {
  initial?: string[]
  disabled?: boolean
  maxItems?: number
  maxItemLength?: number
  onChangeSpy?: (models: string[]) => void
}): React.JSX.Element {
  const [models, setModels] = useState<string[]>(initial)
  return (
    <ModelIdEditor
      value={models}
      disabled={disabled}
      maxItems={maxItems}
      maxItemLength={maxItemLength}
      onChange={(next) => {
        setModels(next)
        onChangeSpy?.(next)
      }}
    />
  )
}

describe('ModelIdEditor', () => {
  it('renders initial chips and count correctly', () => {
    renderWithI18n(<ControlledEditor initial={['gpt-4o-mini', 'deepseek-chat']} />)

    expect(screen.getByText('gpt-4o-mini')).toBeInTheDocument()
    expect(screen.getByText('deepseek-chat')).toBeInTheDocument()
    expect(screen.getByText('2 / 32')).toBeInTheDocument()
  })

  it('adds model on Enter and clears the input', async () => {
    const onChangeSpy = vi.fn()
    renderWithI18n(<ControlledEditor initial={['gpt-4o']} onChangeSpy={onChangeSpy} />)
    const user = userEvent.setup()

    const input = screen.getByRole('textbox', { name: 'Models' })
    await user.type(input, 'claude-3-5-sonnet{Enter}')

    expect(screen.getByText('claude-3-5-sonnet')).toBeInTheDocument()
    expect(input).toHaveValue('')
    expect(screen.getByText('2 / 32')).toBeInTheDocument()
    expect(onChangeSpy).toHaveBeenCalledWith(['gpt-4o', 'claude-3-5-sonnet'])
  })

  it('adds model on comma key', async () => {
    const onChangeSpy = vi.fn()
    renderWithI18n(<ControlledEditor onChangeSpy={onChangeSpy} />)
    const user = userEvent.setup()

    const input = screen.getByRole('textbox', { name: 'Models' })
    await user.type(input, 'qwen-plus,')

    expect(screen.getByText('qwen-plus')).toBeInTheDocument()
    expect(input).toHaveValue('')
    expect(onChangeSpy).toHaveBeenCalledWith(['qwen-plus'])
  })

  it('adds model via clicking the plus button', async () => {
    const onChangeSpy = vi.fn()
    renderWithI18n(<ControlledEditor onChangeSpy={onChangeSpy} />)
    const user = userEvent.setup()

    const input = screen.getByRole('textbox', { name: 'Models' })
    const addBtn = screen.getByRole('button', { name: 'Add model' })
    expect(addBtn).toBeDisabled()

    await user.type(input, 'glm-4')
    expect(addBtn).toBeEnabled()
    await user.click(addBtn)

    expect(screen.getByText('glm-4')).toBeInTheDocument()
    expect(input).toHaveValue('')
    expect(addBtn).toBeDisabled()
    expect(onChangeSpy).toHaveBeenCalledWith(['glm-4'])
  })

  it('removes model when clicking chip remove button', async () => {
    const onChangeSpy = vi.fn()
    renderWithI18n(<ControlledEditor initial={['gpt-4o-mini', 'deepseek-chat']} onChangeSpy={onChangeSpy} />)
    const user = userEvent.setup()

    const removeBtn = screen.getByRole('button', { name: 'Remove model gpt-4o-mini' })
    await user.click(removeBtn)

    expect(screen.queryByText('gpt-4o-mini')).not.toBeInTheDocument()
    expect(screen.getByText('deepseek-chat')).toBeInTheDocument()
    expect(screen.getByText('1 / 32')).toBeInTheDocument()
    expect(onChangeSpy).toHaveBeenCalledWith(['deepseek-chat'])
  })

  it('removes last chip on Backspace when input is empty', async () => {
    const onChangeSpy = vi.fn()
    renderWithI18n(<ControlledEditor initial={['m1', 'm2']} onChangeSpy={onChangeSpy} />)
    const user = userEvent.setup()

    const input = screen.getByRole('textbox', { name: 'Models' })
    await user.click(input)
    await user.keyboard('{Backspace}')

    expect(screen.getByText('m1')).toBeInTheDocument()
    expect(screen.queryByText('m2')).not.toBeInTheDocument()
    expect(onChangeSpy).toHaveBeenCalledWith(['m1'])
  })

  it('does not remove chip on Backspace when input contains text', async () => {
    renderWithI18n(<ControlledEditor initial={['m1']} />)
    const user = userEvent.setup()

    const input = screen.getByRole('textbox', { name: 'Models' })
    await user.type(input, 'abc{Backspace}')

    expect(input).toHaveValue('ab')
    expect(screen.getByText('m1')).toBeInTheDocument()
  })

  it('clears uncommitted input and error on Escape', async () => {
    renderWithI18n(<ControlledEditor initial={['m1']} />)
    const user = userEvent.setup()

    const input = screen.getByRole('textbox', { name: 'Models' })
    await user.type(input, 'm1{Enter}') // duplicate error
    expect(screen.getByText('This model already exists')).toBeInTheDocument()

    await user.type(input, 'other')
    await user.keyboard('{Escape}')
    expect(input).toHaveValue('')
    expect(screen.queryByText('This model already exists')).not.toBeInTheDocument()
  })

  it('commits model on input blur if non-empty', async () => {
    const onChangeSpy = vi.fn()
    renderWithI18n(
      <div>
        <ControlledEditor onChangeSpy={onChangeSpy} />
        <button type="button">Outside</button>
      </div>
    )
    const user = userEvent.setup()

    const input = screen.getByRole('textbox', { name: 'Models' })
    await user.type(input, 'llama-3')
    await user.click(screen.getByRole('button', { name: 'Outside' }))

    expect(screen.getByText('llama-3')).toBeInTheDocument()
    expect(onChangeSpy).toHaveBeenCalledWith(['llama-3'])
  })

  it('parses pasted multiline or comma separated text and trims whitespace', async () => {
    const onChangeSpy = vi.fn()
    renderWithI18n(<ControlledEditor onChangeSpy={onChangeSpy} />)
    const user = userEvent.setup()

    const input = screen.getByRole('textbox', { name: 'Models' })
    await user.click(input)
    await user.paste('gpt-4o-mini\n gpt-4.1,  o3-mini ')

    expect(screen.getByText('gpt-4o-mini')).toBeInTheDocument()
    expect(screen.getByText('gpt-4.1')).toBeInTheDocument()
    expect(screen.getByText('o3-mini')).toBeInTheDocument()
    expect(screen.getByText('3 / 32')).toBeInTheDocument()
    expect(onChangeSpy).toHaveBeenCalledWith(['gpt-4o-mini', 'gpt-4.1', 'o3-mini'])
  })

  it('rejects duplicates and shows inline error', async () => {
    renderWithI18n(<ControlledEditor initial={['gpt-4o']} />)
    const user = userEvent.setup()

    const input = screen.getByRole('textbox', { name: 'Models' })
    await user.type(input, 'gpt-4o{Enter}')

    expect(screen.getByText('This model already exists')).toBeInTheDocument()
    expect(screen.getByText('1 / 32')).toBeInTheDocument()
  })

  it('rejects models exceeding maxItemLength', async () => {
    renderWithI18n(<ControlledEditor maxItemLength={10} />)
    const user = userEvent.setup()

    const input = screen.getByRole('textbox', { name: 'Models' })
    await user.type(input, 'super-long-model-name{Enter}')

    expect(screen.getByText('Model IDs cannot exceed 512 characters')).toBeInTheDocument()
    expect(screen.getByText('0 / 32')).toBeInTheDocument()
  })

  it('enforces maxItems limit and disables input/add', async () => {
    renderWithI18n(<ControlledEditor maxItems={2} initial={['m1']} />)
    const user = userEvent.setup()

    const input = screen.getByRole('textbox', { name: 'Models' })
    await user.type(input, 'm2{Enter}')

    expect(screen.getByText('2 / 2')).toBeInTheDocument()
    expect(input).toBeDisabled()
    expect(input).toHaveAttribute('placeholder', 'Up to 32 models per provider')
    expect(screen.getByRole('button', { name: 'Add model' })).toBeDisabled()
  })

  it('disables all interactions when disabled prop is true', async () => {
    const onChangeSpy = vi.fn()
    renderWithI18n(<ControlledEditor initial={['m1']} disabled onChangeSpy={onChangeSpy} />)
    const user = userEvent.setup()

    expect(screen.getByRole('textbox', { name: 'Models' })).toBeDisabled()
    expect(screen.getByRole('button', { name: 'Remove model m1' })).toBeDisabled()
    expect(screen.getByRole('button', { name: 'Add model' })).toBeDisabled()

    const removeBtn = screen.getByRole('button', { name: 'Remove model m1' })
    await user.click(removeBtn)
    expect(onChangeSpy).not.toHaveBeenCalled()
  })
})

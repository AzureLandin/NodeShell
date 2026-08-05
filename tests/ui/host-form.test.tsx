// @vitest-environment jsdom
import { describe, expect, it, vi } from 'vitest'
import { screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { HostForm, type HostFormSubmit } from '../../src/renderer/src/components/HostForm'
import { renderWithI18n } from './helpers'
import type { HostInput } from '../../src/shared/types'

/**
 * Host create/edit surface (T1.8.3 / S2.1): validation before submit, auth
 * method switching, and the HostInput DTO never carrying password/key content.
 */

function validFields(): Record<'name' | 'host' | 'port' | 'username', string> {
  return { name: 'My server', host: 'example.com', port: '2222', username: 'azure' }
}

async function fillForm(
  user: ReturnType<typeof userEvent.setup>,
  fields: Record<'name' | 'host' | 'port' | 'username', string>
): Promise<void> {
  await user.type(screen.getByLabelText('Name'), fields.name)
  await user.type(screen.getByLabelText('Host'), fields.host)
  await user.clear(screen.getByLabelText('Port'))
  await user.type(screen.getByLabelText('Port'), fields.port)
  await user.type(screen.getByLabelText('Username'), fields.username)
}

describe('HostForm create mode', () => {
  it('blocks submit when required fields are empty', async () => {
    const onSubmit = vi.fn()
    const onCancel = vi.fn()
    renderWithI18n(<HostForm onSubmit={onSubmit} onCancel={onCancel} />)
    const user = userEvent.setup()

    await user.click(screen.getByRole('button', { name: 'Create & connect' }))

    expect(onSubmit).not.toHaveBeenCalled()
  })

  it('rejects an out-of-range port with an inline error', async () => {
    const onSubmit = vi.fn()
    renderWithI18n(<HostForm onSubmit={onSubmit} onCancel={vi.fn()} />)
    const user = userEvent.setup()
    await fillForm(user, { ...validFields(), port: '70000' })

    await user.click(screen.getByRole('button', { name: 'Create & connect' }))

    expect(screen.getByRole('alert')).toHaveTextContent('Port must be between 1 and 65535')
    expect(onSubmit).not.toHaveBeenCalled()
  })

  it('requires a password when authMethod is password', async () => {
    const onSubmit = vi.fn()
    renderWithI18n(<HostForm onSubmit={onSubmit} onCancel={vi.fn()} />)
    const user = userEvent.setup()
    await fillForm(user, validFields())

    await user.click(screen.getByRole('button', { name: 'Create & connect' }))

    expect(screen.getByRole('alert')).toHaveTextContent('Password is required')
    expect(onSubmit).not.toHaveBeenCalled()
  })

  it('switches auth mode between password and private key fields', async () => {
    renderWithI18n(<HostForm onSubmit={vi.fn()} onCancel={vi.fn()} />)
    const user = userEvent.setup()
    await fillForm(user, validFields())

    expect(screen.getByLabelText('Password')).toBeInTheDocument()
    await user.click(screen.getByLabelText('Authentication'))
    await user.click(await screen.findByRole('option', { name: 'Private key' }))

    expect(screen.getByLabelText('Private key path')).toBeInTheDocument()
    expect(screen.queryByLabelText('Password')).not.toBeInTheDocument()
  })

  it('submits the host DTO without any password field', async () => {
    const onSubmit = vi.fn()
    renderWithI18n(<HostForm onSubmit={onSubmit} onCancel={vi.fn()} />)
    const user = userEvent.setup()
    await fillForm(user, validFields())
    await user.type(screen.getByLabelText('Password'), 's3cret')

    await user.click(screen.getByRole('button', { name: 'Create & connect' }))

    await waitFor(() => expect(onSubmit).toHaveBeenCalledTimes(1))
    const result = onSubmit.mock.calls[0][0] as HostFormSubmit
    expect(result.input).toEqual<HostInput>({
      name: 'My server',
      host: 'example.com',
      port: 2222,
      username: 'azure',
      authMethod: 'password'
    })
    expect(result.password).toBe('s3cret')
    expect('password' in result.input).toBe(false)
  })

  it('submits only a private key path in private key mode, never a password', async () => {
    const onSubmit = vi.fn()
    renderWithI18n(<HostForm onSubmit={onSubmit} onCancel={vi.fn()} />)
    const user = userEvent.setup()
    await fillForm(user, validFields())
    await user.click(screen.getByLabelText('Authentication'))
    await user.click(await screen.findByRole('option', { name: 'Private key' }))
    await user.type(screen.getByLabelText('Private key path'), '/home/azure/.ssh/id_ed25519')

    await user.click(screen.getByRole('button', { name: 'Create & connect' }))

    await waitFor(() => expect(onSubmit).toHaveBeenCalledTimes(1))
    const result = onSubmit.mock.calls[0][0] as HostFormSubmit
    expect(result.input).toMatchObject({
      authMethod: 'privateKey',
      privateKeyPath: '/home/azure/.ssh/id_ed25519'
    })
    expect(result.password).toBeUndefined()
    expect('password' in result.input).toBe(false)
  })
})

describe('HostForm edit mode', () => {
  const initial = {
    id: 'h1',
    name: 'Prod',
    host: '10.0.0.1',
    port: 22,
    username: 'root',
    authMethod: 'password' as const
  }

  it('pre-fills the initial values and saves without requiring a password', async () => {
    const onSubmit = vi.fn()
    renderWithI18n(<HostForm initial={initial} onSubmit={onSubmit} onCancel={vi.fn()} />)
    const user = userEvent.setup()

    expect(screen.getByLabelText('Name')).toHaveValue('Prod')
    expect(screen.getByLabelText('Host')).toHaveValue('10.0.0.1')
    expect(screen.getByLabelText('Port')).toHaveValue(22)
    expect(screen.getByLabelText('Username')).toHaveValue('root')

    await user.click(screen.getByRole('button', { name: 'Save' }))

    await waitFor(() => expect(onSubmit).toHaveBeenCalledTimes(1))
    const result = onSubmit.mock.calls[0][0] as HostFormSubmit
    expect(result.input).toEqual<HostInput>({
      name: 'Prod',
      host: '10.0.0.1',
      port: 22,
      username: 'root',
      authMethod: 'password'
    })
    expect(result.password).toBeUndefined()
  })

  it('surfaces a rejected save as an inline error and does not close the form', async () => {
    const onSubmit = vi.fn().mockRejectedValue(new Error('backend refused'))
    renderWithI18n(<HostForm initial={initial} onSubmit={onSubmit} onCancel={vi.fn()} />)
    const user = userEvent.setup()

    await user.click(screen.getByRole('button', { name: 'Save' }))

    expect(await screen.findByRole('alert')).toHaveTextContent('backend refused')
  })
})

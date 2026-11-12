// @vitest-environment jsdom
import { describe, expect, it, vi } from 'vitest'
import { act, fireEvent, screen, waitFor } from '@testing-library/react'
import zh from '../../src/renderer/src/i18n/locales/zh.json'
import { toIpcThrownError } from '../../src/shared/ipc-error'
import userEvent from '@testing-library/user-event'
import { HostForm, type HostFormSubmit } from '../../src/renderer/src/features/hosts/HostForm'
import { createTestI18n, installFakeApi, renderWithI18n } from '../support/helpers'
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
    expect(screen.getByLabelText('Name')).toHaveAccessibleDescription('Name is required')
    expect(screen.getByLabelText('Host')).toHaveAccessibleDescription('Host is required')
    expect(screen.getByLabelText('Username')).toHaveAccessibleDescription('Username is required')
    expect(screen.getByLabelText('Name')).toHaveFocus()
  })

  it('rejects an out-of-range port with an inline error', async () => {
    const onSubmit = vi.fn()
    renderWithI18n(<HostForm onSubmit={onSubmit} onCancel={vi.fn()} />)
    const user = userEvent.setup()
    await fillForm(user, { ...validFields(), port: '70000' })

    await user.click(screen.getByRole('button', { name: 'Create & connect' }))

    expect(screen.getByLabelText('Port')).toHaveAccessibleDescription(
      'Port must be between 1 and 65535'
    )
    expect(screen.getByLabelText('Port')).toHaveAttribute('aria-invalid', 'true')
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

describe('HostForm lifecycle and validation', () => {
  const initial = {
    id: 'h1',
    name: 'Prod',
    host: 'example.com',
    port: 22,
    username: 'root',
    authMethod: 'password' as const
  }

  it('rejects fractional ports, focuses the field, and clears its associated error on correction', async () => {
    const user = userEvent.setup()
    const onSubmit = vi.fn()
    renderWithI18n(<HostForm initial={initial} onSubmit={onSubmit} onCancel={vi.fn()} />)
    await user.clear(screen.getByLabelText('Port'))
    await user.type(screen.getByLabelText('Port'), '22.5')
    await user.click(screen.getByRole('button', { name: 'Save' }))
    expect(onSubmit).not.toHaveBeenCalled()
    expect(screen.getByLabelText('Port')).toHaveFocus()
    expect(screen.getByLabelText('Port')).toHaveAccessibleDescription(
      'Port must be between 1 and 65535'
    )
    expect(screen.getByLabelText('Password')).not.toHaveAttribute('aria-invalid', 'true')
    await user.clear(screen.getByLabelText('Port'))
    await user.type(screen.getByLabelText('Port'), '2222')
    await waitFor(() => expect(screen.queryByRole('alert')).not.toBeInTheDocument())
    await user.click(screen.getByRole('button', { name: 'Save' }))
    expect(onSubmit).toHaveBeenCalledWith({
      input: {
        name: 'Prod',
        host: 'example.com',
        port: 2222,
        username: 'root',
        authMethod: 'password'
      }
    })
  })

  it('clears password drafts when switching authentication, while preserving the nonsecret key path', async () => {
    const user = userEvent.setup()
    const onSubmit = vi.fn()
    renderWithI18n(<HostForm onSubmit={onSubmit} onCancel={vi.fn()} />)
    await fillForm(user, validFields())
    await user.type(screen.getByLabelText('Password'), 'discard me')
    await user.click(screen.getByLabelText('Authentication'))
    await user.click(await screen.findByRole('option', { name: 'Private key' }))
    await user.type(screen.getByLabelText('Private key path'), '/key')
    await user.click(screen.getByLabelText('Authentication'))
    await user.click(await screen.findByRole('option', { name: 'Password' }))
    expect(screen.getByLabelText('Password')).toHaveValue('')
    await user.click(screen.getByRole('button', { name: 'Create & connect' }))
    expect(screen.getByLabelText('Password')).toHaveAccessibleDescription('Password is required')
    await user.type(screen.getByLabelText('Password'), 'new secret')
    await user.click(screen.getByRole('button', { name: 'Create & connect' }))
    expect(onSubmit).toHaveBeenCalledWith({
      input: {
        name: 'My server',
        host: 'example.com',
        port: 2222,
        username: 'azure',
        authMethod: 'password'
      },
      password: 'new secret'
    })
    expect(screen.getByLabelText('Password')).toHaveValue('')
  })

  it('blocks same-tick submits and all auth controls while saving, then preserves the draft for retry', async () => {
    let rejectSave!: (error: Error) => void
    const pending = new Promise<void>((_, reject) => {
      rejectSave = reject
    })
    const onSubmit = vi.fn().mockReturnValueOnce(pending).mockResolvedValue(undefined)
    const user = userEvent.setup()
    renderWithI18n(<HostForm initial={initial} onSubmit={onSubmit} onCancel={vi.fn()} />)
    await user.type(screen.getByLabelText('Password'), 'retry secret')
    const form = screen.getByLabelText('Name').closest('form')!
    fireEvent.submit(form)
    fireEvent.submit(form)
    await waitFor(() => expect(onSubmit).toHaveBeenCalledTimes(1))
    expect(screen.getByLabelText('Name')).toBeDisabled()
    expect(screen.getByLabelText('Password')).toBeDisabled()
    expect(screen.getByLabelText('Authentication')).toBeDisabled()
    expect(screen.getByRole('button', { name: 'Cancel' })).toBeDisabled()
    await act(async () => {
      rejectSave(new Error('backend refused'))
    })
    expect(await screen.findByRole('alert')).toHaveTextContent('backend refused')
    expect(screen.getByLabelText('Password')).toHaveValue('retry secret')
    expect(screen.getByLabelText('Authentication')).not.toBeDisabled()
    await user.click(screen.getByRole('button', { name: 'Save' }))
    expect(onSubmit).toHaveBeenCalledTimes(2)
    expect((onSubmit.mock.calls[1][0] as HostFormSubmit).password).toBe('retry secret')
    expect(screen.getByLabelText('Password')).toHaveValue('')
  })

  it('preserves drafts on ordinary parent renders and resets fields, secrets and errors on identity changes', async () => {
    const onSubmit = vi.fn().mockRejectedValue(new Error('old error'))
    const onCancel = vi.fn()
    const user = userEvent.setup()
    const view = renderWithI18n(
      <HostForm initial={initial} onSubmit={onSubmit} onCancel={onCancel} />
    )
    const rerender = (target?: typeof initial): void =>
      view.rerender(<HostForm initial={target} onSubmit={onSubmit} onCancel={onCancel} />)
    await user.type(screen.getByLabelText('Name'), ' draft')
    await user.type(screen.getByLabelText('Password'), 'secret')
    await user.click(screen.getByRole('button', { name: 'Save' }))
    expect(await screen.findByRole('alert')).toHaveTextContent('old error')
    rerender({ ...initial })
    expect(screen.getByLabelText('Name')).toHaveValue('Prod draft')
    expect(screen.getByLabelText('Password')).toHaveValue('secret')
    rerender({ ...initial, id: 'h2', name: 'Other' })
    expect(screen.getByLabelText('Name')).toHaveValue('Other')
    expect(screen.getByLabelText('Password')).toHaveValue('')
    expect(screen.queryByRole('alert')).not.toBeInTheDocument()
    rerender()
    expect(screen.getByLabelText('Name')).toHaveValue('')
    expect(screen.getByLabelText('Port')).toHaveValue(22)
    expect(screen.getByLabelText('Password')).toHaveValue('')
  })

  it('omits a blank existing key path on edit so backend credentials remain unchanged', async () => {
    const onSubmit = vi.fn()
    const user = userEvent.setup()
    renderWithI18n(
      <HostForm
        initial={{
          ...initial,
          authMethod: 'privateKey',
          privateKeyPath: '/saved/key',
          credentialsSaved: true
        }}
        onSubmit={onSubmit}
        onCancel={vi.fn()}
      />
    )
    await user.clear(screen.getByLabelText('Private key path'))
    await user.click(screen.getByRole('button', { name: 'Save' }))
    expect(onSubmit).toHaveBeenCalledWith({
      input: {
        name: 'Prod',
        host: 'example.com',
        port: 22,
        username: 'root',
        authMethod: 'privateKey'
      }
    })
  })

  it('requires a key path when changing an edit without an existing path to private key', async () => {
    const onSubmit = vi.fn()
    const user = userEvent.setup()
    renderWithI18n(<HostForm initial={initial} onSubmit={onSubmit} onCancel={vi.fn()} />)
    await user.click(screen.getByLabelText('Authentication'))
    await user.click(await screen.findByRole('option', { name: 'Private key' }))
    await user.click(screen.getByRole('button', { name: 'Save' }))
    expect(screen.getByLabelText('Private key path')).toHaveAccessibleDescription(
      'Select a private key file'
    )
    expect(screen.getByLabelText('Private key path')).toHaveFocus()
    expect(onSubmit).not.toHaveBeenCalled()
  })

  it('accepts only the latest file picker result and discards responses after auth change or unmount', async () => {
    const fake = installFakeApi()
    const user = userEvent.setup()
    const pending: ((path: string) => void)[] = []
    fake.mocks.dialog.openPrivateKeyFile.mockImplementation(
      () => new Promise<string>((resolve) => pending.push(resolve))
    )
    const view = renderWithI18n(
      <HostForm
        initial={{ ...initial, authMethod: 'privateKey' }}
        onSubmit={vi.fn()}
        onCancel={vi.fn()}
      />
    )
    await user.click(screen.getByRole('button', { name: 'Browse' }))
    await user.click(screen.getByRole('button', { name: 'Browse' }))
    await act(async () => {
      pending[1]('/new/key')
      pending[0]('/stale/key')
    })
    expect(screen.getByLabelText('Private key path')).toHaveValue('/new/key')
    await user.click(screen.getByRole('button', { name: 'Browse' }))
    await user.click(screen.getByLabelText('Authentication'))
    await user.click(await screen.findByRole('option', { name: 'Password' }))
    await act(async () => {
      pending[2]('/late/key')
    })
    await user.click(screen.getByLabelText('Authentication'))
    await user.click(await screen.findByRole('option', { name: 'Private key' }))
    expect(screen.getByLabelText('Private key path')).toHaveValue('/new/key')
    await user.click(screen.getByRole('button', { name: 'Browse' }))
    view.unmount()
    await act(async () => {
      pending[3]('/unmounted/key')
    })
    renderWithI18n(
      <HostForm
        initial={{ ...initial, authMethod: 'privateKey' }}
        onSubmit={vi.fn()}
        onCancel={vi.fn()}
      />
    )
    expect(screen.getByLabelText('Private key path')).toHaveValue('')
  })

  it('keeps IME composition text intact and sends the completed field text', async () => {
    const onSubmit = vi.fn()
    const user = userEvent.setup()
    renderWithI18n(<HostForm initial={initial} onSubmit={onSubmit} onCancel={vi.fn()} />)
    const name = screen.getByLabelText('Name')
    fireEvent.compositionStart(name)
    fireEvent.change(name, { target: { value: '生产' } })
    fireEvent.compositionEnd(name, { data: '生产' })
    expect(name).toHaveValue('生产')
    await user.click(screen.getByRole('button', { name: 'Save' }))
    expect((onSubmit.mock.calls[0][0] as HostFormSubmit).input.name).toBe('生产')
  })

  it('parses backend IPC errors for inline display and permits a corrected retry', async () => {
    const onSubmit = vi
      .fn()
      .mockRejectedValueOnce(
        toIpcThrownError({ code: 'CONFIG_WRITE_FAILED', message: 'write denied' })
      )
      .mockResolvedValue(undefined)
    const user = userEvent.setup()
    renderWithI18n(<HostForm initial={initial} onSubmit={onSubmit} onCancel={vi.fn()} />)
    await user.click(screen.getByRole('button', { name: 'Save' }))
    expect(await screen.findByRole('alert')).toHaveTextContent('write denied')
    expect(screen.getByRole('alert')).not.toHaveTextContent('NODESHELL_ERR')
    await user.type(screen.getByLabelText('Name'), ' corrected')
    expect(screen.queryByRole('alert')).not.toBeInTheDocument()
    await user.click(screen.getByRole('button', { name: 'Save' }))
    expect(onSubmit).toHaveBeenCalledTimes(2)
  })

  it('localizes field errors after a language change without resetting the draft', async () => {
    const onSubmit = vi.fn()
    const onCancel = vi.fn()
    const user = userEvent.setup()
    const i18n = createTestI18n()
    i18n.addResourceBundle('zh', 'translation', zh)
    const view = renderWithI18n(
      <HostForm initial={initial} onSubmit={onSubmit} onCancel={onCancel} />,
      i18n
    )
    await user.clear(screen.getByLabelText('Port'))
    await user.type(screen.getByLabelText('Port'), '70000')
    await user.click(screen.getByRole('button', { name: 'Save' }))
    expect(screen.getByLabelText('Port')).toHaveAccessibleDescription(
      'Port must be between 1 and 65535'
    )
    await act(async () => {
      await i18n.changeLanguage('zh')
    })
    view.rerender(<HostForm initial={initial} onSubmit={onSubmit} onCancel={onCancel} />)
    expect(screen.getByLabelText(zh.form.port)).toHaveValue(70000)
    expect(screen.getByLabelText(zh.form.port)).toHaveAccessibleDescription(zh.form.portInvalid)
  })

  it('ignores validation and save results belonging to a replaced form', async () => {
    let fail!: (error: Error) => void
    const onSubmit = vi.fn().mockImplementation(
      () =>
        new Promise<void>((_, reject) => {
          fail = reject
        })
    )
    const onCancel = vi.fn()
    const view = renderWithI18n(
      <HostForm initial={initial} onSubmit={onSubmit} onCancel={onCancel} />
    )
    const renderTarget = (id: string): React.JSX.Element => (
      <HostForm initial={{ ...initial, id }} onSubmit={onSubmit} onCancel={onCancel} />
    )
    fireEvent.submit(screen.getByLabelText('Name').closest('form')!)
    view.rerender(renderTarget('h2'))
    await act(async () => undefined)
    expect(onSubmit).not.toHaveBeenCalled()
    await userEvent.setup().click(screen.getByRole('button', { name: 'Save' }))
    await waitFor(() => expect(onSubmit).toHaveBeenCalledTimes(1))
    view.rerender(renderTarget('h3'))
    await act(async () => {
      fail(new Error('old failure'))
    })
    expect(screen.queryByRole('alert')).not.toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Save' })).not.toBeDisabled()
  })
})

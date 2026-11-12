// @vitest-environment jsdom
import { describe, expect, it, vi } from 'vitest'
import { act, fireEvent, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { HostPickerModal } from '../../src/renderer/src/features/hosts/HostPickerModal'
import { renderWithI18n } from '../support/helpers'
import type { HostConfig } from '../../src/shared/types'

describe('HostPickerModal empty state and interactions', () => {
  it('renders title block with subtitle and single New Host button in empty state', () => {
    renderWithI18n(
      <HostPickerModal
        hosts={[]}
        onConnect={vi.fn()}
        onCreate={vi.fn()}
        onUpdate={vi.fn()}
        onRemove={vi.fn()}
        onClose={vi.fn()}
      />
    )

    expect(screen.getByRole('heading', { name: 'Host Manager' })).toBeInTheDocument()
    expect(screen.getByText('Manage saved remote hosts and connections')).toBeInTheDocument()

    // Exactly one "New host" button exists (in header), no duplicate in the empty state
    const newButtons = screen.getAllByRole('button', { name: /New host/ })
    expect(newButtons).toHaveLength(1)

    // Empty state title and hint
    expect(screen.getByText('No saved hosts yet')).toBeInTheDocument()
    expect(
      screen.getByText("Click 'New host' in the top right to add your first server")
    ).toBeInTheDocument()
  })

  it('opens create form when clicking New host in header and cancels back to empty state', async () => {
    const user = userEvent.setup()
    renderWithI18n(
      <HostPickerModal
        hosts={[]}
        onConnect={vi.fn()}
        onCreate={vi.fn()}
        onUpdate={vi.fn()}
        onRemove={vi.fn()}
        onClose={vi.fn()}
      />
    )

    await user.click(screen.getByRole('button', { name: /New host/ }))

    // Form inputs appear
    expect(screen.getByLabelText('Name')).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Cancel' })).toBeInTheDocument()

    // Cancel exits form back to empty state
    await user.click(screen.getByRole('button', { name: 'Cancel' }))
    expect(screen.getByText('No saved hosts yet')).toBeInTheDocument()
  })

  it('triggers onClose when clicking dismiss button', async () => {
    const onClose = vi.fn()
    const user = userEvent.setup()
    renderWithI18n(
      <HostPickerModal
        hosts={[]}
        onConnect={vi.fn()}
        onCreate={vi.fn()}
        onUpdate={vi.fn()}
        onRemove={vi.fn()}
        onClose={onClose}
      />
    )

    await user.click(screen.getByRole('button', { name: 'Dismiss' }))
    expect(onClose).toHaveBeenCalledTimes(1)
  })

  it('renders host items and actions when hosts exist', async () => {
    const sampleHost: HostConfig = {
      id: 'h1',
      name: 'Prod Server',
      host: '192.168.1.100',
      port: 22,
      username: 'root',
      authMethod: 'password',
      credentialsSaved: true
    }

    const onConnect = vi.fn()
    const user = userEvent.setup()
    renderWithI18n(
      <HostPickerModal
        hosts={[sampleHost]}
        onConnect={onConnect}
        onCreate={vi.fn()}
        onUpdate={vi.fn()}
        onRemove={vi.fn()}
        onClose={vi.fn()}
      />
    )

    expect(screen.getByText('Prod Server')).toBeInTheDocument()
    expect(screen.getByText(/192\.168\.1\.100/)).toBeInTheDocument()
    expect(screen.queryByText('No saved hosts yet')).not.toBeInTheDocument()

    await user.click(screen.getByRole('button', { name: 'Connect' }))
    expect(onConnect).toHaveBeenCalledWith(sampleHost)
  })

  it('supports search filtering, selecting for edit, and deleting with confirmation', async () => {
    const hosts: HostConfig[] = [
      {
        id: 'h1',
        name: 'Web Server',
        host: 'web.example.com',
        port: 22,
        username: 'ubuntu',
        authMethod: 'password',
        credentialsSaved: true
      },
      {
        id: 'h2',
        name: 'Database Node',
        host: 'db.example.com',
        port: 2200,
        username: 'postgres',
        authMethod: 'privateKey',
        credentialsSaved: false
      }
    ]

    const onRemove = vi.fn()
    const user = userEvent.setup()
    renderWithI18n(
      <HostPickerModal
        hosts={hosts}
        onConnect={vi.fn()}
        onCreate={vi.fn()}
        onUpdate={vi.fn()}
        onRemove={onRemove}
        onClose={vi.fn()}
      />
    )

    // Search filter
    const searchInput = screen.getByPlaceholderText('Search hosts…')
    await user.type(searchInput, 'database')
    expect(screen.getByText('Database Node')).toBeInTheDocument()
    expect(screen.queryByText('Web Server')).not.toBeInTheDocument()

    // Clear search
    await user.click(screen.getByRole('button', { name: 'Clear search' }))
    expect(screen.getByText('Web Server')).toBeInTheDocument()
    expect(screen.getByText('Database Node')).toBeInTheDocument()

    // Select host for edit
    await user.click(screen.getByText('Web Server'))
    expect(screen.getByLabelText('Name')).toHaveValue('Web Server')
    expect(screen.getByLabelText('Host')).toHaveValue('web.example.com')

    // Delete host
    await user.click(screen.getAllByRole('button', { name: 'Delete' })[0])
    expect(screen.getByText(/Delete host/i)).toBeInTheDocument()
    await user.click(screen.getByRole('button', { name: 'OK' }))
    await waitFor(() => expect(onRemove).toHaveBeenCalledWith('h1'))
  })
})

describe('HostPickerModal form lifecycle', () => {
  const hosts: HostConfig[] = [
    {
      id: 'h1',
      name: 'First',
      host: 'first.example.com',
      port: 22,
      username: 'root',
      authMethod: 'password',
      credentialsSaved: true
    },
    {
      id: 'h2',
      name: 'Second',
      host: 'second.example.com',
      port: 2222,
      username: 'user',
      authMethod: 'password'
    }
  ]

  it('keeps create mode distinct from an imported host whose id is create', async () => {
    const user = userEvent.setup()
    renderWithI18n(
      <HostPickerModal
        hosts={[{ ...hosts[0], id: 'create' }]}
        onConnect={vi.fn()}
        onCreate={vi.fn()}
        onUpdate={vi.fn()}
        onRemove={vi.fn()}
        onClose={vi.fn()}
      />
    )
    await user.click(screen.getByText('First'))
    await user.type(screen.getByLabelText('Password'), 'discard me')
    await user.click(screen.getByRole('button', { name: /New host/ }))
    expect(screen.getByLabelText('Name')).toHaveValue('')
    expect(screen.getByLabelText('Password')).toHaveValue('')
    expect(screen.getByRole('button', { name: 'Create & connect' })).toBeInTheDocument()
  })

  it('protects a pending save from Escape, outside dismissal, repeated submit and target switching', async () => {
    let finish!: () => void
    const onUpdate = vi.fn().mockImplementation(
      () =>
        new Promise<void>((resolve) => {
          finish = resolve
        })
    )
    const onClose = vi.fn()
    const user = userEvent.setup()
    renderWithI18n(
      <HostPickerModal
        hosts={hosts}
        onConnect={vi.fn()}
        onCreate={vi.fn()}
        onUpdate={onUpdate}
        onRemove={vi.fn()}
        onClose={onClose}
      />
    )
    await user.click(screen.getByText('First'))
    await user.click(screen.getByRole('button', { name: 'Save' }))
    await waitFor(() => expect(onUpdate).toHaveBeenCalledTimes(1))
    expect(onUpdate).toHaveBeenCalledWith('h1', {
      input: {
        name: 'First',
        host: 'first.example.com',
        port: 22,
        username: 'root',
        authMethod: 'password'
      }
    })
    expect(screen.getByRole('button', { name: /New host/ })).toBeDisabled()
    expect(screen.getByRole('button', { name: 'Dismiss' })).toBeDisabled()
    expect(
      screen
        .getAllByRole('button', { name: 'Edit' })
        .every((button) => (button as HTMLButtonElement).disabled)
    ).toBe(true)
    await user.click(screen.getByText('Second'))
    expect(screen.getByLabelText('Name')).toHaveValue('First')
    await user.keyboard('{Escape}')
    const overlay = screen.getByRole('dialog').closest('.modal-overlay')!
    fireEvent.pointerDown(overlay)
    fireEvent.click(overlay)
    fireEvent.submit(screen.getByLabelText('Name').closest('form')!)
    expect(onClose).not.toHaveBeenCalled()
    expect(onUpdate).toHaveBeenCalledTimes(1)
    expect(screen.getByLabelText('Name')).toHaveValue('First')
    await act(async () => {
      finish()
    })
    await waitFor(() => expect(screen.queryByLabelText('Name')).not.toBeInTheDocument())
    await user.click(screen.getByText('Second'))
    expect(screen.getByLabelText('Name')).toHaveValue('Second')
    expect(screen.getByLabelText('Password')).toHaveValue('')
    expect(screen.getByRole('button', { name: 'Dismiss' })).not.toBeDisabled()
  })

  it('clears secrets on cancel, Escape, target change and reopening a new-host form', async () => {
    const user = userEvent.setup()
    const onClose = vi.fn()
    renderWithI18n(
      <HostPickerModal
        hosts={hosts}
        onConnect={vi.fn()}
        onCreate={vi.fn()}
        onUpdate={vi.fn()}
        onRemove={vi.fn()}
        onClose={onClose}
      />
    )
    await user.click(screen.getByText('First'))
    await user.type(screen.getByLabelText('Password'), 'first secret')
    await user.click(screen.getByText('Second'))
    expect(screen.getByLabelText('Password')).toHaveValue('')
    await user.type(screen.getByLabelText('Password'), 'second secret')
    await user.click(screen.getByRole('button', { name: 'Cancel' }))
    await user.click(screen.getByText('Second'))
    expect(screen.getByLabelText('Password')).toHaveValue('')
    await user.type(screen.getByLabelText('Password'), 'another secret')
    await user.keyboard('{Escape}')
    expect(screen.queryByLabelText('Name')).not.toBeInTheDocument()
    expect(onClose).not.toHaveBeenCalled()
    await user.click(screen.getAllByRole('button', { name: /New host/ })[0])
    expect(screen.getByLabelText('Name')).toHaveValue('')
    expect(screen.getByLabelText('Password')).toHaveValue('')
  })
})

describe('HostPickerModal query and delete feedback', () => {
  const host: HostConfig = {
    id: 'h1',
    name: 'Server',
    host: 'example.com',
    port: 22,
    username: 'root',
    authMethod: 'password'
  }
  const props = {
    onConnect: vi.fn(),
    onCreate: vi.fn(),
    onUpdate: vi.fn(),
    onRemove: vi.fn(),
    onClose: vi.fn()
  }

  it('shows loading before a list exists, without presenting it as an empty store', () => {
    renderWithI18n(
      <HostPickerModal
        {...props}
        hosts={[]}
        loading
        refreshing
        hasData={false}
        onRefresh={vi.fn()}
      />
    )
    expect(screen.getByRole('status')).toHaveTextContent('Loading hosts')
    expect(screen.queryByText('No saved hosts yet')).not.toBeInTheDocument()
    expect(screen.getByRole('button', { name: /New host/ })).toBeDisabled()
    expect(screen.getByRole('button', { name: 'Refresh hosts' })).toBeDisabled()
  })

  it('shows a read error locally while keeping confirmed hosts available', async () => {
    const onRefresh = vi.fn()
    const user = userEvent.setup()
    renderWithI18n(
      <HostPickerModal {...props} hosts={[host]} loadError="read denied" onRefresh={onRefresh} />
    )
    expect(screen.getByRole('alert')).toHaveTextContent(
      'Successful operations do not need to be repeated'
    )
    expect(screen.getByRole('alert')).toHaveTextContent('read denied')
    expect(screen.getByText('Server')).toBeInTheDocument()
    await user.click(screen.getByRole('button', { name: 'Retry loading hosts' }))
    expect(onRefresh).toHaveBeenCalledTimes(1)
  })

  it('keeps the host and editing draft after delete failure, then permits one explicit retry', async () => {
    let rejectDelete!: (error: Error) => void
    const onRemove = vi
      .fn()
      .mockImplementationOnce(
        () =>
          new Promise<void>((_, reject) => {
            rejectDelete = reject
          })
      )
      .mockResolvedValue(undefined)
    const onClose = vi.fn()
    const user = userEvent.setup()
    renderWithI18n(
      <HostPickerModal {...props} hosts={[host]} onRemove={onRemove} onClose={onClose} />
    )
    await user.click(screen.getByText('Server'))
    await user.type(screen.getByLabelText('Name'), ' draft')
    await user.click(screen.getByRole('button', { name: 'Delete' }))
    await user.click(screen.getByRole('button', { name: 'OK' }))
    await waitFor(() => expect(onRemove).toHaveBeenCalledTimes(1))
    expect(screen.getByRole('button', { name: 'Delete' })).toBeDisabled()
    expect(screen.getByRole('button', { name: 'Dismiss' })).toBeDisabled()
    await user.keyboard('{Escape}')
    expect(onClose).not.toHaveBeenCalled()
    await act(async () => {
      rejectDelete(new Error('NODESHELL_ERR:CONFIG_WRITE_FAILED:delete denied'))
    })
    expect(await screen.findByRole('alert')).toHaveTextContent('delete denied')
    expect(screen.getByRole('alert')).not.toHaveTextContent('NODESHELL_ERR')
    expect(screen.getByLabelText('Name')).toHaveValue('Server draft')
    expect(screen.getByText('Server')).toBeInTheDocument()
    await user.click(screen.getByRole('button', { name: 'Delete' }))
    await user.click(screen.getByRole('button', { name: 'OK' }))
    await waitFor(() => expect(onRemove).toHaveBeenCalledTimes(2))
    await waitFor(() => expect(screen.queryByLabelText('Name')).not.toBeInTheDocument())
    expect(screen.queryByRole('alert')).not.toBeInTheDocument()
  })
})

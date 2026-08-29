// @vitest-environment jsdom
import { describe, expect, it, vi } from 'vitest'
import { screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { HostPickerModal } from '../../src/renderer/src/components/HostPickerModal'
import { renderWithI18n } from './helpers'
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

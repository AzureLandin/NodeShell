// @vitest-environment jsdom
import { describe, expect, it, vi } from 'vitest'
import { act, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import App from '../../src/renderer/src/App'
import { PermissionModal } from '../../src/renderer/src/components/PermissionModal'
import type { PermissionAskEvent } from '../../src/shared/types'
import { emitPermissionAsk, emitPermissionClosed, installFakeApi, renderWithI18n } from './helpers'

function ask(overrides: Partial<PermissionAskEvent> = {}): PermissionAskEvent {
  return {
    id: 'ask-1',
    source: 'agent',
    tool: 'bash',
    sessionId: 's1',
    title: 'prod-web',
    summary: 'uptime',
    ...overrides
  }
}

describe('PermissionModal', () => {
  it('shows the source, host, action and command without treating it as html', () => {
    const onDecide = vi.fn()
    renderWithI18n(
      <PermissionModal
        request={ask({ summary: '<script>x</script>', detail: '12 bytes' })}
        onDecide={onDecide}
      />
    )
    expect(screen.getByRole('dialog', { name: 'Permission required' })).toBeInTheDocument()
    expect(screen.getByText('Agent')).toBeInTheDocument()
    expect(screen.getByText('Host: prod-web')).toBeInTheDocument()
    expect(screen.getByText('Run command')).toBeInTheDocument()
    expect(screen.getByText('<script>x</script>')).toBeInTheDocument()
    expect(screen.getByText('12 bytes')).toBeInTheDocument()
  })

  it('sends deny / allow / allow-session from the three buttons', async () => {
    const onDecide = vi.fn()
    renderWithI18n(<PermissionModal request={ask()} onDecide={onDecide} />)
    const user = userEvent.setup()
    await user.click(screen.getByRole('button', { name: 'Deny' }))
    expect(onDecide).toHaveBeenCalledWith('deny')
    await user.click(screen.getByRole('button', { name: 'Allow for this session' }))
    expect(onDecide).toHaveBeenCalledWith('allow-session')
    await user.click(screen.getByRole('button', { name: 'Allow once' }))
    expect(onDecide).toHaveBeenCalledWith('allow')
  })

  it('puts initial focus on Deny, matching the native prompt', () => {
    renderWithI18n(<PermissionModal request={ask()} onDecide={vi.fn()} />)
    expect(screen.getByRole('button', { name: 'Deny' })).toHaveFocus()
  })

  it('renders the inline variant inside the caller without an overlay', () => {
    renderWithI18n(
      <PermissionModal variant="inline" request={ask()} onDecide={vi.fn()} />
    )
    const dialog = screen.getByRole('dialog', { name: 'Permission required' })
    expect(dialog).toHaveClass('agent-permission')
    expect(dialog.querySelector('.permission-modal-overlay')).toBeNull()
    expect(document.querySelector('.permission-modal-overlay')).toBeNull()
    expect(screen.queryByText('Agent')).not.toBeInTheDocument()
    expect(screen.getByText('Host: prod-web')).toBeInTheDocument()
  })
})

describe('App permission prompt', () => {
  it('opens on permission:ask and answers through PermissionDecide', async () => {
    const fake = installFakeApi()
    fake.mocks.settings.get.mockResolvedValue({
      language: 'en',
      themePreference: 'system',
      terminalFontFamily: 'Hack',
      terminalFontSize: 14,
      mcpIdleTimeoutMinutes: 10,
      mcpMaxSessions: 8,
      permissionPolicy: 'ask'
    })
    fake.mocks.hosts.list.mockResolvedValue([])
    renderWithI18n(<App />)
    await waitFor(() => expect(fake.mocks.permission.onAsk).toHaveBeenCalled())

    emitPermissionAsk(fake, ask({ tool: 'sftp_write', summary: '/tmp/x', detail: '6 bytes' }))
    const dialog = await screen.findByRole('dialog', { name: 'Permission required' })
    expect(dialog.closest('.agent-panel')).toBeTruthy()
    expect(document.querySelector('.permission-modal-overlay')).toBeNull()
    expect(screen.getByText('Write remote file')).toBeInTheDocument()
    expect(screen.getByText('/tmp/x')).toBeInTheDocument()

    const user = userEvent.setup()
    await user.click(screen.getByRole('button', { name: 'Allow once' }))
    expect(fake.mocks.permission.decide).toHaveBeenCalledWith('ask-1', 'allow')
    await waitFor(() =>
      expect(screen.queryByRole('dialog', { name: 'Permission required' })).not.toBeInTheDocument()
    )
  })

  it('keeps MCP asks on the overlay', async () => {
    const fake = installFakeApi()
    fake.mocks.settings.get.mockResolvedValue({
      language: 'en',
      themePreference: 'system',
      terminalFontFamily: 'Hack',
      terminalFontSize: 14,
      mcpIdleTimeoutMinutes: 10,
      mcpMaxSessions: 8
    })
    fake.mocks.hosts.list.mockResolvedValue([])
    renderWithI18n(<App />)
    await waitFor(() => expect(fake.mocks.permission.onAsk).toHaveBeenCalled())

    emitPermissionAsk(fake, ask({ source: 'mcp', title: 'mcp-host' }))
    const dialog = await screen.findByRole('dialog', { name: 'Permission required' })
    expect(dialog.closest('.permission-modal-overlay')).toBeTruthy()
    expect(dialog.closest('.agent-panel')).toBeNull()
    expect(screen.getByText('MCP client')).toBeInTheDocument()
  })

  it('reopens the agent dock when an agent ask arrives while it is hidden', async () => {
    const fake = installFakeApi()
    fake.mocks.settings.get.mockResolvedValue({
      language: 'en',
      themePreference: 'system',
      terminalFontFamily: 'Hack',
      terminalFontSize: 14,
      mcpIdleTimeoutMinutes: 10,
      mcpMaxSessions: 8
    })
    fake.mocks.hosts.list.mockResolvedValue([])
    renderWithI18n(<App />)
    await waitFor(() => expect(fake.mocks.permission.onAsk).toHaveBeenCalled())

    const user = userEvent.setup()
    await user.click(screen.getByRole('button', { name: 'Hide Agent' }))
    expect(document.querySelector('.agent-dock')).toHaveClass('is-collapsed')

    act(() => {
      emitPermissionAsk(fake, ask())
    })
    expect(document.querySelector('.agent-dock')).not.toHaveClass('is-collapsed')
    const dialog = await screen.findByRole('dialog', { name: 'Permission required' })
    expect(dialog.closest('.agent-panel')).toBeTruthy()
  })

  it('dismisses the prompt when the backend cancels the ask', async () => {
    const fake = installFakeApi()
    fake.mocks.settings.get.mockResolvedValue({
      language: 'en',
      themePreference: 'system',
      terminalFontFamily: 'Hack',
      terminalFontSize: 14,
      mcpIdleTimeoutMinutes: 10,
      mcpMaxSessions: 8
    })
    fake.mocks.hosts.list.mockResolvedValue([])
    renderWithI18n(<App />)
    await waitFor(() => expect(fake.mocks.permission.onAsk).toHaveBeenCalled())

    emitPermissionAsk(fake, ask())
    expect(await screen.findByRole('dialog', { name: 'Permission required' })).toBeInTheDocument()
    emitPermissionClosed(fake, { id: 'ask-1' })
    await waitFor(() =>
      expect(screen.queryByRole('dialog', { name: 'Permission required' })).not.toBeInTheDocument()
    )
    expect(fake.mocks.permission.decide).not.toHaveBeenCalled()
  })
})

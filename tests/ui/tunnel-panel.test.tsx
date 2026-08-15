// @vitest-environment jsdom
import { describe, expect, it } from 'vitest'
import { screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { SidebarPanel } from '../../src/renderer/src/components/SidebarPanel'
import { TunnelPanel } from '../../src/renderer/src/components/TunnelPanel'
import { installFakeApi, renderWithI18n } from './helpers'

describe('TunnelPanel', () => {
  it('asks for a session when disconnected', () => {
    installFakeApi()
    renderWithI18n(<TunnelPanel activeSessionId={null} connected={false} />)
    expect(screen.getByText('Connect a session to forward remote ports.')).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Refresh' })).toBeDisabled()
    expect(screen.getByRole('button', { name: 'Forward selected' })).toBeDisabled()
  })

  it('lists remote listeners and forwards a selected port', async () => {
    const fake = installFakeApi()
    fake.mocks.tunnels.discover.mockResolvedValue([{ bind: '0.0.0.0', port: 8080 }])
    fake.mocks.tunnels.list.mockResolvedValue([])
    fake.mocks.tunnels.start.mockResolvedValue({
      id: 'tun-1',
      sessionId: 's1',
      localHost: '127.0.0.1',
      localPort: 8080,
      remoteAddr: '0.0.0.0',
      remotePort: 8080
    })
    renderWithI18n(<TunnelPanel activeSessionId="s1" connected />)

    const checkbox = await screen.findByRole('checkbox', { name: 'Port 8080 on 0.0.0.0' })
    expect(fake.mocks.tunnels.discover).toHaveBeenCalledWith('s1')

    const user = userEvent.setup()
    await user.click(screen.getByRole('button', { name: 'Forward' }))
    await waitFor(() =>
      expect(fake.mocks.tunnels.start).toHaveBeenCalledWith('s1', '0.0.0.0', 8080)
    )
    expect(await screen.findByText('127.0.0.1:8080')).toBeInTheDocument()

    await user.click(checkbox)
    expect(checkbox).toBeChecked()
  })

  it('stops a live forward', async () => {
    const fake = installFakeApi()
    fake.mocks.tunnels.discover.mockResolvedValue([{ bind: '127.0.0.1', port: 3000 }])
    fake.mocks.tunnels.list.mockResolvedValue([
      {
        id: 'tun-9',
        sessionId: 's1',
        localHost: '127.0.0.1',
        localPort: 3000,
        remoteAddr: '127.0.0.1',
        remotePort: 3000
      }
    ])
    renderWithI18n(<TunnelPanel activeSessionId="s1" connected />)

    const user = userEvent.setup()
    await user.click(await screen.findByRole('button', { name: 'Stop' }))
    await waitFor(() => expect(fake.mocks.tunnels.stop).toHaveBeenCalledWith('s1', 'tun-9'))
    await waitFor(() => expect(screen.queryByText('127.0.0.1:3000')).not.toBeInTheDocument())
  })
})

describe('SidebarPanel ports tab', () => {
  it('switches from monitor to the ports panel', async () => {
    const fake = installFakeApi()
    fake.mocks.tunnels.discover.mockResolvedValue([])
    renderWithI18n(
      <SidebarPanel activeSessionId="s1" activeSessionTitle="prod-web" connected />
    )
    expect(screen.getByRole('heading', { name: 'Monitor' })).toBeInTheDocument()

    const user = userEvent.setup()
    await user.click(screen.getByRole('tab', { name: 'Ports' }))
    expect(await screen.findByRole('button', { name: 'Refresh' })).toBeInTheDocument()
    expect(await screen.findByText('No listening TCP ports found.')).toBeInTheDocument()
    expect(within(screen.getByRole('tablist')).getByRole('tab', { name: 'Ports' })).toHaveAttribute(
      'aria-selected',
      'true'
    )
  })

  it('keeps the monitor poller running while the ports tab is showing', async () => {
    const fake = installFakeApi()
    fake.mocks.tunnels.discover.mockResolvedValue([])
    renderWithI18n(
      <SidebarPanel activeSessionId="s1" activeSessionTitle="prod-web" connected />
    )
    await waitFor(() =>
      expect(fake.mocks.monitor.setActive).toHaveBeenCalledWith('s1', 'prod-web')
    )
    const calls = fake.mocks.monitor.setActive.mock.calls.length

    const user = userEvent.setup()
    await user.click(screen.getByRole('tab', { name: 'Ports' }))
    await user.click(screen.getByRole('tab', { name: 'Monitor' }))

    expect(fake.mocks.monitor.setActive).toHaveBeenCalledTimes(calls)
    expect(fake.mocks.monitor.setActive).not.toHaveBeenCalledWith(null)
    expect(screen.getByRole('heading', { name: 'Monitor' })).toBeVisible()
  })
})

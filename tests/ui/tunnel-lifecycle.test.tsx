// @vitest-environment jsdom
import { act, fireEvent, screen, waitFor } from '@testing-library/react'
import { describe, expect, it } from 'vitest'
import { TunnelPanel } from '../../src/renderer/src/features/tunnels'
import type { Tunnel, TunnelListener } from '../../src/shared/types'
import { deferred } from '../support/deferred'
import { installFakeApi, renderWithI18n } from '../support/helpers'

const listeners = [
  { bind: '127.0.0.1', port: 3000 },
  { bind: '127.0.0.1', port: 8080 }
]
function tunnel(sessionId: string, localPort = 3000): Tunnel {
  return {
    id: sessionId,
    sessionId,
    localHost: '127.0.0.1',
    localPort,
    remoteAddr: '127.0.0.1',
    remotePort: 3000
  }
}

describe('port forwarding request lifetime', () => {
  it('ignores old discovery results after a session switch', async () => {
    const fake = installFakeApi()
    const old = deferred<TunnelListener[]>()
    fake.mocks.tunnels.discover
      .mockReturnValueOnce(old.promise)
      .mockResolvedValue([{ bind: '127.0.0.1', port: 9000 }])
    const view = renderWithI18n(<TunnelPanel activeSessionId="s1" connected />)
    view.rerender(<TunnelPanel activeSessionId="s2" connected />)
    await screen.findByText('9000')
    await act(async () => old.resolve(listeners))
    expect(screen.queryByText('3000')).not.toBeInTheDocument()
    expect(screen.getByText('9000')).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Refresh' })).toBeEnabled()
  })

  it('isolates a failed command and its cleanup from a pending command in the new session', async () => {
    const fake = installFakeApi()
    fake.mocks.tunnels.discover.mockResolvedValue([listeners[0]])
    const old = deferred<Tunnel>()
    const current = deferred<Tunnel>()
    fake.mocks.tunnels.start.mockReturnValueOnce(old.promise).mockReturnValueOnce(current.promise)
    const view = renderWithI18n(<TunnelPanel activeSessionId="s1" connected />)
    fireEvent.click(await screen.findByRole('button', { name: 'Forward' }))
    view.rerender(<TunnelPanel activeSessionId="s2" connected />)
    fireEvent.click(await screen.findByRole('button', { name: 'Forward' }))
    await act(async () => old.reject(new Error('old start failed')))
    expect(screen.queryByText('old start failed')).not.toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Forward' })).toBeDisabled()
    await act(async () => current.resolve(tunnel('s2', 4500)))
    expect(await screen.findByText('127.0.0.1:4500')).toBeInTheDocument()
    expect(fake.mocks.tunnels.start).toHaveBeenCalledTimes(2)
  })

  it('stops an unfinished batch when the session disconnects and resets selection', async () => {
    const fake = installFakeApi()
    fake.mocks.tunnels.discover.mockResolvedValue(listeners)
    const first = deferred<Tunnel>()
    fake.mocks.tunnels.start.mockReturnValue(first.promise)
    const view = renderWithI18n(<TunnelPanel activeSessionId="s1" connected />)
    for (const box of await screen.findAllByRole('checkbox')) fireEvent.click(box)
    fireEvent.click(screen.getByRole('button', { name: 'Forward selected' }))
    expect(fake.mocks.tunnels.start).toHaveBeenCalledTimes(1)
    view.rerender(<TunnelPanel activeSessionId="s1" connected={false} />)
    await act(async () => first.resolve(tunnel('s1')))
    expect(fake.mocks.tunnels.start).toHaveBeenCalledTimes(1)
    view.rerender(<TunnelPanel activeSessionId="s1" connected />)
    await waitFor(() => expect(screen.getAllByRole('checkbox')).toHaveLength(2))
    for (const box of screen.getAllByRole('checkbox')) expect(box).not.toBeChecked()
    expect(screen.queryByText('127.0.0.1:3000')).not.toBeInTheDocument()
  })

  it('does not let an old stop remove a tunnel in a new session', async () => {
    const fake = installFakeApi()
    fake.mocks.tunnels.discover.mockResolvedValue([listeners[0]])
    fake.mocks.tunnels.list
      .mockResolvedValueOnce([tunnel('s1')])
      .mockResolvedValue([tunnel('s2', 4500)])
    const stop = deferred<void>()
    fake.mocks.tunnels.stop.mockReturnValue(stop.promise)
    const view = renderWithI18n(<TunnelPanel activeSessionId="s1" connected />)
    fireEvent.click(await screen.findByRole('button', { name: 'Stop' }))
    view.rerender(<TunnelPanel activeSessionId="s2" connected />)
    await screen.findByText('127.0.0.1:4500')
    await act(async () => stop.resolve(undefined))
    expect(screen.getByText('127.0.0.1:4500')).toBeInTheDocument()
  })

  it('does not enqueue the rest of a batch after unmount', async () => {
    const fake = installFakeApi()
    fake.mocks.tunnels.discover.mockResolvedValue(listeners)
    const first = deferred<Tunnel>()
    fake.mocks.tunnels.start.mockReturnValue(first.promise)
    const view = renderWithI18n(<TunnelPanel activeSessionId="s1" connected />)
    for (const box of await screen.findAllByRole('checkbox')) fireEvent.click(box)
    fireEvent.click(screen.getByRole('button', { name: 'Forward selected' }))
    view.unmount()
    await act(async () => first.resolve(tunnel('s1')))
    expect(fake.mocks.tunnels.start).toHaveBeenCalledTimes(1)
  })

  it('submits a forward only once before React paints the busy state', async () => {
    const fake = installFakeApi()
    fake.mocks.tunnels.discover.mockResolvedValue([listeners[0]])
    fake.mocks.tunnels.start.mockResolvedValue(tunnel('s1'))
    renderWithI18n(<TunnelPanel activeSessionId="s1" connected />)
    const button = await screen.findByRole('button', { name: 'Forward' })
    // A command starts synchronously before React has painted its busy state.
    fireEvent.click(button)
    fireEvent.click(button)
    expect(fake.mocks.tunnels.start).toHaveBeenCalledTimes(1)
    expect(await screen.findByText('127.0.0.1:3000')).toBeInTheDocument()
  })
})

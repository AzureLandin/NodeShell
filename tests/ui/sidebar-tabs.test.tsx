// @vitest-environment jsdom
import { describe, expect, it, vi } from 'vitest'
import { act, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { SidebarPanel } from '../../src/renderer/src/app/SidebarPanel'
import type { MonitorSnapshot, MonitorUpdateEvent } from '../../src/shared/types'
import { installFakeApi, renderWithI18n } from '../support/helpers'

describe('SidebarPanel tabs', () => {
  it('supports manual keyboard activation and keeps tab panels associated', async () => {
    installFakeApi()
    const user = userEvent.setup()

    renderWithI18n(<SidebarPanel activeSessionId="s1" activeSessionTitle="prod-web" connected />)

    const tablist = screen.getByRole('tablist', { name: 'App' })
    const monitorTab = within(tablist).getByRole('tab', { name: 'Monitor' })
    const portsTab = within(tablist).getByRole('tab', { name: 'Ports' })
    const monitorPanel = screen.getByRole('tabpanel', { name: 'Monitor' })
    const portsPanel = document.getElementById(portsTab.getAttribute('aria-controls') ?? '')
    expect(portsPanel).not.toBeNull()

    expect(monitorTab).toHaveAttribute('aria-selected', 'true')
    expect(portsTab).toHaveAttribute('aria-selected', 'false')
    expect(monitorTab).toHaveAttribute('aria-controls', monitorPanel.id)
    expect(monitorPanel).toHaveAttribute('aria-labelledby', monitorTab.id)
    expect(portsTab).toHaveAttribute('aria-controls', portsPanel!.id)
    expect(portsPanel).toHaveAttribute('aria-labelledby', portsTab.id)
    expect(monitorPanel).toBeVisible()
    expect(monitorPanel).not.toHaveAttribute('hidden')
    expect(monitorPanel).not.toHaveAttribute('inert')
    expect(portsPanel).toHaveAttribute('hidden')
    expect(portsPanel).toHaveAttribute('inert')

    monitorTab.focus()
    await user.keyboard('{ArrowRight}')
    expect(document.activeElement).toBe(portsTab)
    expect(portsTab).toHaveAttribute('aria-selected', 'false')

    await user.keyboard('{Home}')
    expect(document.activeElement).toBe(monitorTab)
    await user.keyboard('{End}')
    expect(document.activeElement).toBe(portsTab)
    expect(portsTab).toHaveAttribute('aria-selected', 'false')

    await user.keyboard('{Enter}')
    expect(portsTab).toHaveAttribute('aria-selected', 'true')
    expect(portsPanel).toBeVisible()
    expect(portsPanel).not.toHaveAttribute('hidden')
    expect(portsPanel).not.toHaveAttribute('inert')
    expect(monitorPanel).toHaveAttribute('hidden')
    expect(monitorPanel).toHaveAttribute('inert')
  })

  it('updates the hidden monitor and unsubscribes only on unmount', async () => {
    const fake = installFakeApi()
    const unsubscribe = vi.fn()
    let emitUpdate: ((event: MonitorUpdateEvent) => void) | undefined
    fake.mocks.monitor.onUpdate.mockImplementation((callback) => {
      emitUpdate = callback as (event: MonitorUpdateEvent) => void
      return unsubscribe
    })
    const user = userEvent.setup()
    const { unmount } = renderWithI18n(
      <SidebarPanel activeSessionId="s1" activeSessionTitle="prod-web" connected />
    )

    const tablist = screen.getByRole('tablist', { name: 'App' })
    const monitorTab = within(tablist).getByRole('tab', { name: 'Monitor' })
    const portsTab = within(tablist).getByRole('tab', { name: 'Ports' })
    await waitFor(() => expect(fake.mocks.monitor.onUpdate).toHaveBeenCalledTimes(1))
    expect(emitUpdate).toBeTypeOf('function')

    await user.click(portsTab)
    const snapshot: MonitorSnapshot = {
      title: 'edge-after-switch',
      cpuPercent: 18,
      memUsedBytes: 64,
      memTotalBytes: 128,
      swapUsedBytes: 0,
      swapTotalBytes: 0,
      load1: 0.1,
      load5: 0.2,
      load15: 0.3,
      netRxBps: 1,
      netTxBps: 2,
      processes: [],
      updatedAt: 1
    }
    act(() => {
      emitUpdate?.({ sessionId: 's1', snapshot })
    })

    const hiddenMonitorPanel = document.getElementById(
      monitorTab.getAttribute('aria-controls') ?? ''
    )
    expect(hiddenMonitorPanel).not.toBeNull()
    expect(hiddenMonitorPanel).toHaveTextContent('edge-after-switch')
    expect(unsubscribe).not.toHaveBeenCalled()
    expect(fake.mocks.monitor.onUpdate).toHaveBeenCalledTimes(1)

    await user.click(monitorTab)
    expect(screen.getByRole('tabpanel', { name: 'Monitor' })).toHaveTextContent('edge-after-switch')
    expect(unsubscribe).not.toHaveBeenCalled()
    expect(fake.mocks.monitor.onUpdate).toHaveBeenCalledTimes(1)

    unmount()
    expect(unsubscribe).toHaveBeenCalledTimes(1)
  })
})

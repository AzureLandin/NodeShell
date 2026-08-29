// @vitest-environment jsdom
import { describe, expect, it, vi } from 'vitest'
import { act, fireEvent, renderHook, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { TransferCenter } from '../../src/renderer/src/components/TransferCenter'
import {
  formatBytes,
  formatEta,
  formatSpeed,
  useTransferTasks
} from '../../src/renderer/src/hooks/useTransferTasks'
import { emitTransferEvent, installFakeApi, renderWithI18n } from './helpers'
import type { TransferTask } from '../../src/shared/types'

describe('Transfer formatters', () => {
  it('formatBytes handles zero, small, and large bytes', () => {
    expect(formatBytes(0)).toBe('0 B')
    expect(formatBytes(-5)).toBe('0 B')
    expect(formatBytes(512)).toBe('512 B')
    expect(formatBytes(1024)).toBe('1.0 KB')
    expect(formatBytes(1536)).toBe('1.5 KB')
    expect(formatBytes(10 * 1024 * 1024)).toBe('10 MB')
    expect(formatBytes(1.5 * 1024 * 1024 * 1024)).toBe('1.5 GB')
  })

  it('formatSpeed appends /s', () => {
    expect(formatSpeed(0)).toBe('0 B/s')
    expect(formatSpeed(1024 * 1024)).toBe('1.0 MB/s')
  })

  it('formatEta handles finalizing, calculating, almost done, seconds and minutes', () => {
    expect(formatEta(0, true)).toBe('finalizing')
    expect(formatEta(null)).toBe('calculating')
    expect(formatEta(1)).toBe('almostDone')
    expect(formatEta(15)).toBe('15s')
    expect(formatEta(75)).toBe('1m 15s')
  })
})

describe('useTransferTasks hook', () => {
  it('loads snapshot and updates active count on transfer events', async () => {
    const fake = installFakeApi()
    const task1: TransferTask = {
      taskId: 't-1',
      sessionId: 's-1',
      sessionTitle: 'Host 1',
      direction: 'upload',
      name: 'file1.zip',
      remotePath: '/remote/file1.zip',
      transferred: 0,
      total: 1000,
      state: 'queued',
      createdAt: 1000
    }
    fake.mocks.transfer.getTasks.mockResolvedValue([task1])

    const { result } = renderHook(() => useTransferTasks(false))

    await waitFor(() => expect(result.current.tasks.length).toBe(1))
    expect(result.current.activeCount).toBe(1)
    expect(result.current.tasks[0].taskId).toBe('t-1')

    // Emit running event
    act(() => {
      emitTransferEvent(fake, {
        ...task1,
        state: 'running',
        transferred: 500
      })
    })

    expect(result.current.tasks[0].state).toBe('running')
    expect(result.current.tasks[0].transferred).toBe(500)
    expect(result.current.activeCount).toBe(1)

    // Emit succeeded event
    act(() => {
      emitTransferEvent(fake, {
        ...task1,
        state: 'succeeded',
        transferred: 1000,
        finishedAt: Date.now()
      })
    })

    expect(result.current.tasks[0].state).toBe('succeeded')
    expect(result.current.activeCount).toBe(0)
  })

  it('prevents state reversion from terminal state to queued/running', async () => {
    const fake = installFakeApi()
    fake.mocks.transfer.getTasks.mockResolvedValue([])

    const { result } = renderHook(() => useTransferTasks(false))

    const task: TransferTask = {
      taskId: 't-1',
      sessionId: 's-1',
      sessionTitle: 'Host 1',
      direction: 'upload',
      name: 'file1.zip',
      remotePath: '/remote/file1.zip',
      transferred: 1000,
      total: 1000,
      state: 'succeeded',
      createdAt: 1000,
      finishedAt: 2000
    }

    act(() => {
      emitTransferEvent(fake, task)
    })

    expect(result.current.tasks[0].state).toBe('succeeded')

    // Late queued event arrives
    act(() => {
      emitTransferEvent(fake, {
        ...task,
        state: 'queued',
        transferred: 0,
        finishedAt: undefined
      })
    })

    // Must still be succeeded with transferred 1000 and finishedAt 2000
    expect(result.current.tasks[0].state).toBe('succeeded')
    expect(result.current.tasks[0].transferred).toBe(1000)
    expect(result.current.tasks[0].finishedAt).toBe(2000)
  })

  it('forwards cancel, retry, clear and clearCompleted', async () => {
    const fake = installFakeApi()
    fake.mocks.transfer.getTasks.mockResolvedValue([
      {
        taskId: 't-1',
        sessionId: 's-1',
        sessionTitle: 'Host 1',
        direction: 'upload',
        name: 'file1.zip',
        remotePath: '/remote/file1.zip',
        transferred: 0,
        total: 1000,
        state: 'running',
        createdAt: 1000
      }
    ])

    const { result } = renderHook(() => useTransferTasks(false))
    await waitFor(() => expect(result.current.tasks.length).toBe(1))

    // Cancel
    await act(async () => {
      await result.current.cancel('t-1')
    })
    expect(fake.mocks.transfer.cancel).toHaveBeenCalledWith('t-1')

    // Retry
    await act(async () => {
      await result.current.retry('t-1')
    })
    expect(fake.mocks.transfer.retry).toHaveBeenCalledWith('t-1')

    // Clear
    await act(async () => {
      await result.current.clear('t-1')
    })
    expect(fake.mocks.transfer.clear).toHaveBeenCalledWith('t-1')
    expect(result.current.tasks.length).toBe(0)

    // Clear completed
    await act(async () => {
      await result.current.clearCompleted()
    })
    expect(fake.mocks.transfer.clearCompleted).toHaveBeenCalled()
  })

  it('pauses auto-dismissal when isPaused is true', async () => {
    vi.useFakeTimers()
    const fake = installFakeApi()
    const now = Date.now()
    fake.mocks.transfer.getTasks.mockResolvedValue([
      {
        taskId: 't-done',
        sessionId: 's-1',
        sessionTitle: 'Host 1',
        direction: 'upload',
        name: 'done.txt',
        remotePath: '/remote/done.txt',
        transferred: 100,
        total: 100,
        state: 'succeeded',
        createdAt: now - 20000,
        finishedAt: now - 15000 // elapsed > 8000ms
      }
    ])

    // Paused hook
    const { result } = renderHook(() => useTransferTasks(true))
    await act(async () => {
      await Promise.resolve()
    })

    act(() => {
      vi.advanceTimersByTime(5000)
    })

    // Should still exist because isPaused is true
    expect(result.current.tasks.length).toBe(1)

    vi.useRealTimers()
  })
})

describe('TransferCenter UI component', () => {
  it('renders trigger button with active badge and toggles popover', async () => {
    const fake = installFakeApi()
    fake.mocks.transfer.getTasks.mockResolvedValue([
      {
        taskId: 't-1',
        sessionId: 's-1',
        sessionTitle: 'Production Server',
        direction: 'upload',
        name: 'archive.tar.gz',
        remotePath: '/var/archive.tar.gz',
        transferred: 2500000,
        total: 10000000,
        state: 'running',
        createdAt: 1000
      }
    ])

    renderWithI18n(<TransferCenter />)
    const user = userEvent.setup()

    // Trigger button should display active badge "1" after snapshot resolves
    expect(await screen.findByText('1')).toBeInTheDocument()
    const toggleBtn = screen.getByRole('button', { name: /Transfers/i })
    expect(toggleBtn).toBeInTheDocument()

    // Popover is initially not in document
    expect(screen.queryByRole('region', { name: /Transfers/i })).not.toBeInTheDocument()

    // Click toggle to open popover
    await user.click(toggleBtn)

    const region = await screen.findByRole('region', { name: /Transfers/i })
    expect(region).toBeInTheDocument()
    // Verify portal renders directly under document.body
    expect(region.parentElement).toBe(document.body)
    expect(region.style.position).toBe('fixed')
    expect(screen.getByText('archive.tar.gz')).toBeInTheDocument()
    expect(screen.getByText('Production Server')).toBeInTheDocument()
    expect(screen.getByText('Transferring')).toBeInTheDocument()
    expect(screen.getByText(/2.4 MB \/ 9.5 MB \(25%\)/)).toBeInTheDocument()

    // Press Escape to close
    fireEvent.keyDown(document, { key: 'Escape' })
    expect(screen.queryByRole('region', { name: /Transfers/i })).not.toBeInTheDocument()
  })

  it('closes popover when clicking outside the portal popover and toggle button', async () => {
    const fake = installFakeApi()
    fake.mocks.transfer.getTasks.mockResolvedValue([])

    renderWithI18n(
      <div>
        <div data-testid="outside-area">Outside</div>
        <TransferCenter />
      </div>
    )
    const user = userEvent.setup()
    const toggleBtn = screen.getByRole('button', { name: /Transfers/i })

    await user.click(toggleBtn)
    expect(await screen.findByRole('region', { name: /Transfers/i })).toBeInTheDocument()

    // Click outside area
    fireEvent.mouseDown(screen.getByTestId('outside-area'))
    expect(screen.queryByRole('region', { name: /Transfers/i })).not.toBeInTheDocument()
  })

  it('does not render cancel button in finalizing state', async () => {
    const fake = installFakeApi()
    fake.mocks.transfer.getTasks.mockResolvedValue([
      {
        taskId: 't-fin',
        sessionId: 's-1',
        sessionTitle: 'Host A',
        direction: 'upload',
        name: 'large.iso',
        remotePath: '/remote/large.iso',
        transferred: 1000,
        total: 1000,
        state: 'finalizing',
        createdAt: 1000
      }
    ])

    renderWithI18n(<TransferCenter />)
    const user = userEvent.setup()
    const toggleBtn = await screen.findByRole('button', { name: /Transfers/i })
    await user.click(toggleBtn)

    expect(screen.getAllByText('Finalizing').length).toBeGreaterThanOrEqual(1)
    expect(screen.queryByRole('button', { name: /Cancel/i })).not.toBeInTheDocument()
  })

  it('shows empty state when there are no tasks', async () => {
    const fake = installFakeApi()
    fake.mocks.transfer.getTasks.mockResolvedValue([])

    renderWithI18n(<TransferCenter />)
    const user = userEvent.setup()

    await user.click(screen.getByRole('button', { name: /Transfers/i }))
    expect(screen.getByText('No file transfers')).toBeInTheDocument()
  })

  it('renders failed task with error box and retry button', async () => {
    const fake = installFakeApi()
    fake.mocks.transfer.getTasks.mockResolvedValue([
      {
        taskId: 't-err',
        sessionId: 's-1',
        sessionTitle: 'Host A',
        direction: 'download',
        name: 'database.dump',
        remotePath: '/var/db/database.dump',
        transferred: 500,
        total: 1000,
        state: 'failed',
        error: 'Permission denied (remote server)',
        createdAt: 1000,
        finishedAt: 2000
      }
    ])

    renderWithI18n(<TransferCenter />)
    const user = userEvent.setup()

    await user.click(screen.getByRole('button', { name: /Transfers/i }))

    expect(screen.getByText('database.dump')).toBeInTheDocument()
    expect(screen.getByText('Failed')).toBeInTheDocument()
    expect(screen.getByText('Permission denied (remote server)')).toBeInTheDocument()

    const retryBtn = screen.getByRole('button', { name: /Retry/i })
    await user.click(retryBtn)
    expect(fake.mocks.transfer.retry).toHaveBeenCalledWith('t-err')
  })

  it('triggers clearCompleted when header button is clicked', async () => {
    const fake = installFakeApi()
    fake.mocks.transfer.getTasks.mockResolvedValue([
      {
        taskId: 't-done',
        sessionId: 's-1',
        sessionTitle: 'Host A',
        direction: 'upload',
        name: 'done.txt',
        remotePath: '/remote/done.txt',
        transferred: 100,
        total: 100,
        state: 'succeeded',
        createdAt: 1000,
        finishedAt: 2000
      }
    ])

    renderWithI18n(<TransferCenter />)
    const user = userEvent.setup()

    await user.click(screen.getByRole('button', { name: /Transfers/i }))
    const clearBtn = screen.getByRole('button', { name: /Clear completed/i })
    await user.click(clearBtn)

    expect(fake.mocks.transfer.clearCompleted).toHaveBeenCalledTimes(1)
  })
})

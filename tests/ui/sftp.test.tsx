// @vitest-environment jsdom
import { describe, expect, it, vi } from 'vitest'
import { act, fireEvent, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { SftpPanel } from '../../src/renderer/src/components/SftpPanel'
import { emitTransferEvent, installFakeApi, renderWithI18n } from './helpers'

/**
 * SFTP surface (T1.8.3 / S2.1): Wails native file drops upload exactly once
 * via files.onDrop -> sftp.uploadPaths, the DOM drop fallback is suppressed
 * while onDrop exists, subscriptions are cleaned up, and listing/navigation
 * render through the real API calls.
 */

type SftpEntry = {
  name: string
  path: string
  isDirectory: boolean
  size: number
  modifyTime: number
}

function listEntries(): SftpEntry[] {
  return [
    { name: 'docs', path: '/home/user/docs', isDirectory: true, size: 0, modifyTime: 0 },
    {
      name: 'readme.md',
      path: '/home/user/readme.md',
      isDirectory: false,
      size: 2048,
      modifyTime: 0
    }
  ]
}

async function renderExpandedPanel(): Promise<
  ReturnType<typeof renderWithI18n> & { fake: ReturnType<typeof installFakeApi> }
> {
  const fake = installFakeApi()
  fake.mocks.sftp.cwd.mockResolvedValue('/home/user')
  fake.mocks.sftp.list.mockResolvedValue(listEntries())
  const utils = renderWithI18n(<SftpPanel sessionId="s1" connected expanded onToggle={vi.fn()} />)
  await screen.findByText('docs')
  return { fake, ...utils }
}

describe('SftpPanel listing', () => {
  it('does not fetch while collapsed', () => {
    const fake = installFakeApi()
    renderWithI18n(<SftpPanel sessionId="s1" connected expanded={false} onToggle={vi.fn()} />)

    expect(fake.mocks.sftp.cwd).not.toHaveBeenCalled()
    expect(fake.mocks.sftp.list).not.toHaveBeenCalled()
    expect(screen.getByRole('button', { name: /Files \(SFTP\)/ })).not.toHaveTextContent('/')
  })

  it('loads cwd and entries through the API when expanded', async () => {
    const { fake } = await renderExpandedPanel()

    expect(screen.getByLabelText('Directory')).toHaveValue('/home/user')
    expect(screen.getByText('docs')).toBeInTheDocument()
    expect(screen.getByText('readme.md')).toBeInTheDocument()
    expect(fake.mocks.sftp.cwd).toHaveBeenCalledWith('s1')
    expect(fake.mocks.sftp.list).toHaveBeenCalledWith('s1')
    expect(screen.getByRole('button', { name: /Files \(SFTP\)/ })).not.toHaveTextContent(
      '/home/user'
    )
  })

  it('shows the placeholder when no session is connected', () => {
    installFakeApi()
    renderWithI18n(<SftpPanel sessionId={null} connected={false} expanded onToggle={vi.fn()} />)

    expect(screen.getByText('Connect a session to use SFTP')).toBeInTheDocument()
  })

  it('double-clicking a directory navigates via chdir and reloads the listing', async () => {
    const fake = installFakeApi()
    fake.mocks.sftp.cwd.mockResolvedValue('/home/user/docs')
    fake.mocks.sftp.list.mockResolvedValueOnce(listEntries())
    fake.mocks.sftp.list.mockResolvedValueOnce([
      {
        name: 'report.txt',
        path: '/home/user/docs/report.txt',
        isDirectory: false,
        size: 5,
        modifyTime: 0
      }
    ])
    fake.mocks.sftp.chdir.mockResolvedValue('/home/user/docs')
    renderWithI18n(<SftpPanel sessionId="s1" connected expanded onToggle={vi.fn()} />)
    await screen.findByText('docs')

    fireEvent.doubleClick(screen.getByText('docs'))

    await waitFor(() => expect(fake.mocks.sftp.chdir).toHaveBeenCalledWith('s1', 'docs'))
    expect(await screen.findByText('report.txt')).toBeInTheDocument()
    expect(screen.getByLabelText('Directory')).toHaveValue('/home/user/docs')
  })

  it('navigates when an absolute path is submitted from the path field', async () => {
    const fake = installFakeApi()
    fake.mocks.sftp.cwd.mockResolvedValue('/home/user')
    fake.mocks.sftp.list.mockResolvedValueOnce(listEntries())
    fake.mocks.sftp.list.mockResolvedValueOnce([
      {
        name: 'hosts',
        path: '/etc/hosts',
        isDirectory: false,
        size: 12,
        modifyTime: 0
      }
    ])
    fake.mocks.sftp.chdir.mockResolvedValue('/etc')
    renderWithI18n(<SftpPanel sessionId="s1" connected expanded onToggle={vi.fn()} />)
    await screen.findByText('docs')

    const input = screen.getByLabelText('Directory')
    const user = userEvent.setup()
    await user.clear(input)
    await user.type(input, '/etc')
    await user.keyboard('{Enter}')

    await waitFor(() => expect(fake.mocks.sftp.chdir).toHaveBeenCalledWith('s1', '/etc'))
    expect(await screen.findByText('hosts')).toBeInTheDocument()
    expect(screen.getByLabelText('Directory')).toHaveValue('/etc')
  })

  it('restores the current directory when Escape is pressed in the path field', async () => {
    await renderExpandedPanel()
    const input = screen.getByLabelText('Directory')
    const user = userEvent.setup()
    await user.clear(input)
    await user.type(input, '/tmp')
    await user.keyboard('{Escape}')

    expect(input).toHaveValue('/home/user')
  })

  it('restores the current directory when chdir from the path field fails', async () => {
    const fake = installFakeApi()
    fake.mocks.sftp.cwd.mockResolvedValue('/home/user')
    fake.mocks.sftp.list.mockResolvedValue(listEntries())
    fake.mocks.sftp.chdir.mockRejectedValue(new Error('no such file'))
    renderWithI18n(<SftpPanel sessionId="s1" connected expanded onToggle={vi.fn()} />)
    await screen.findByText('docs')

    const input = screen.getByLabelText('Directory')
    const user = userEvent.setup()
    await user.clear(input)
    await user.type(input, '/does/not/exist')
    await user.keyboard('{Enter}')

    await waitFor(() => expect(fake.mocks.sftp.chdir).toHaveBeenCalledWith('s1', '/does/not/exist'))
    await waitFor(() => expect(input).toHaveValue('/home/user'))
    expect(await screen.findByText('no such file')).toBeInTheDocument()
    expect(screen.getByText('docs')).toBeInTheDocument()
  })

  it('toggle button reports expand/collapse intent', async () => {
    installFakeApi()
    const onToggle = vi.fn()
    renderWithI18n(
      <SftpPanel sessionId={null} connected={false} expanded={false} onToggle={onToggle} />
    )
    const user = userEvent.setup()

    await user.click(screen.getByRole('button', { name: /Files \(SFTP\)/ }))

    expect(onToggle).toHaveBeenCalledTimes(1)
  })
})

describe('SftpPanel file drops (Wails native path)', () => {
  it('uploads native drop paths exactly once and suppresses the DOM drop fallback', async () => {
    const { fake } = await renderExpandedPanel()

    // Native drop: the component subscribed to files.onDrop at mount.
    expect(fake.mocks.files.onDrop).toHaveBeenCalledTimes(1)
    const nativeDrop = fake.mocks.files.onDrop.mock.calls[0][0]
    await act(async () => {
      nativeDrop(['C:\\local\\a.txt'])
    })
    await waitFor(() =>
      expect(fake.mocks.transfer.enqueueUpload).toHaveBeenCalledWith('s1', '/home/user', ['C:\\local\\a.txt'])
    )
    expect(fake.mocks.transfer.enqueueUpload).toHaveBeenCalledTimes(1)

    // DOM drop fallback must be suppressed while files.onDrop exists — no
    // second upload even though the drop carries File objects. jsdom has no
    // DataTransfer constructor, so hand a minimal drop payload.
    const dataTransfer = {
      files: [new File(['content'], 'f.txt')],
      types: ['Files'],
      dropEffect: 'none'
    } as unknown as DataTransfer
    fireEvent.drop(document.querySelector('.sftp-panel-body') as HTMLElement, {
      dataTransfer
    })

    await waitFor(() => expect(fake.mocks.transfer.enqueueUpload).toHaveBeenCalledTimes(1))
  })

  it('unsubscribes native drop and transfer subscriptions on unmount', async () => {
    const { fake, unmount } = await renderExpandedPanel()
    const dropOff = fake.mocks.files.onDrop.mock.results[0].value
    const transferOff = fake.mocks.transfer.onTask.mock.results[0].value

    unmount()

    expect(dropOff).toHaveBeenCalledTimes(1)
    expect(transferOff).toHaveBeenCalledTimes(1)
  })

  it('falls back to DOM getPathForFile when native onDrop is unavailable', async () => {
    const { fake } = await renderExpandedPanel()
    delete (fake.api.files as { onDrop?: unknown }).onDrop
    const fakeFile = new File(['content'], 'f.txt')
    fake.mocks.files.getPathForFile.mockReturnValue('C:\\local\\f.txt')

    const dataTransfer = {
      files: [fakeFile],
      types: ['Files'],
      dropEffect: 'none'
    } as unknown as DataTransfer
    fireEvent.drop(document.querySelector('.sftp-panel-body') as HTMLElement, {
      dataTransfer
    })

    await waitFor(() =>
      expect(fake.mocks.transfer.enqueueUpload).toHaveBeenCalledWith('s1', '/home/user', ['C:\\local\\f.txt'])
    )
  })

  it('shows error when DOM drop cannot resolve file path and onDrop is unavailable', async () => {
    const { fake } = await renderExpandedPanel()
    delete (fake.api.files as { onDrop?: unknown }).onDrop
    fake.mocks.files.getPathForFile.mockImplementation(() => {
      throw new Error('NOT_IMPLEMENTED')
    })

    const dataTransfer = {
      files: [new File(['content'], 'f.txt')],
      types: ['Files'],
      dropEffect: 'none'
    } as unknown as DataTransfer
    fireEvent.drop(document.querySelector('.sftp-panel-body') as HTMLElement, {
      dataTransfer
    })

    expect(
      await screen.findByText('Only files can be dropped (folders are not supported)')
    ).toBeInTheDocument()
    expect(fake.mocks.transfer.enqueueUpload).not.toHaveBeenCalled()
  })

  it('does not stop propagation on dragover and drop so window listeners receive them', async () => {
    await renderExpandedPanel()
    const target = document.querySelector('.sftp-panel-body') as HTMLElement

    let windowDragOverFired = false
    let windowDropFired = false
    const onWindowDragOver = (): void => {
      windowDragOverFired = true
    }
    const onWindowDrop = (): void => {
      windowDropFired = true
    }

    window.addEventListener('dragover', onWindowDragOver)
    window.addEventListener('drop', onWindowDrop)

    const dataTransfer = {
      files: [],
      types: ['Files'],
      dropEffect: 'none'
    } as unknown as DataTransfer

    fireEvent.dragOver(target, { dataTransfer })
    expect(windowDragOverFired).toBe(true)

    fireEvent.drop(target, { dataTransfer })
    expect(windowDropFired).toBe(true)

    window.removeEventListener('dragover', onWindowDragOver)
    window.removeEventListener('drop', onWindowDrop)
  })

  it('only refreshes directory when an upload task completes for the current directory', async () => {
    const { fake } = await renderExpandedPanel()
    const listCallsBefore = fake.mocks.sftp.list.mock.calls.length

    // Different session
    emitTransferEvent(fake, {
      taskId: 't-other-session',
      sessionId: 's-other',
      sessionTitle: 'Other',
      direction: 'upload',
      name: 'file.txt',
      remotePath: '/home/user',
      transferred: 100,
      total: 100,
      state: 'succeeded',
      createdAt: 1000
    })
    expect(fake.mocks.sftp.list.mock.calls.length).toBe(listCallsBefore)

    // Download task (not upload)
    emitTransferEvent(fake, {
      taskId: 't-dl',
      sessionId: 's1',
      sessionTitle: 'Session 1',
      direction: 'download',
      name: 'file.txt',
      remotePath: '/home/user/file.txt',
      transferred: 100,
      total: 100,
      state: 'succeeded',
      createdAt: 1000
    })
    expect(fake.mocks.sftp.list.mock.calls.length).toBe(listCallsBefore)

    // Upload to a different directory
    emitTransferEvent(fake, {
      taskId: 't-other-dir',
      sessionId: 's1',
      sessionTitle: 'Session 1',
      direction: 'upload',
      name: 'file.txt',
      remotePath: '/var/log',
      transferred: 100,
      total: 100,
      state: 'succeeded',
      createdAt: 1000
    })
    expect(fake.mocks.sftp.list.mock.calls.length).toBe(listCallsBefore)

    // Upload to the current directory (/home/user)
    emitTransferEvent(fake, {
      taskId: 't-match',
      sessionId: 's1',
      sessionTitle: 'Session 1',
      direction: 'upload',
      name: 'file.txt',
      remotePath: '/home/user',
      transferred: 100,
      total: 100,
      state: 'succeeded',
      createdAt: 1000
    })
    await waitFor(() => expect(fake.mocks.sftp.list.mock.calls.length).toBe(listCallsBefore + 1))
  })
})

describe('SftpPanel context menu', () => {
  function row(name: string): HTMLElement {
    const el = screen.getByText(name).closest('li')
    if (!el) throw new Error(`row for "${name}" not found`)
    return el
  }

  it('opens file actions on right-click and has no inline action buttons', async () => {
    const { fake } = await renderExpandedPanel()
    fake.mocks.transfer.chooseDownloadTarget.mockResolvedValue('task-1')

    expect(screen.queryByRole('button', { name: 'Download' })).not.toBeInTheDocument()
    expect(screen.queryByRole('button', { name: 'Rename' })).not.toBeInTheDocument()
    expect(screen.queryByRole('button', { name: 'Delete' })).not.toBeInTheDocument()

    fireEvent.contextMenu(row('readme.md'))
    expect(screen.getByRole('menuitem', { name: 'Edit' })).toBeInTheDocument()
    expect(screen.getByRole('menuitem', { name: 'Download' })).toBeInTheDocument()
    expect(screen.getByRole('menuitem', { name: 'Rename' })).toBeInTheDocument()
    expect(screen.getByRole('menuitem', { name: 'Delete' })).toBeInTheDocument()

    fireEvent.click(screen.getByRole('menuitem', { name: 'Download' }))
    await waitFor(() =>
      expect(fake.mocks.transfer.chooseDownloadTarget).toHaveBeenCalledWith('s1', '/home/user/readme.md', 'readme.md')
    )
  })

  it('omits Edit and Download for directories', async () => {
    await renderExpandedPanel()

    fireEvent.contextMenu(row('docs'))
    expect(screen.queryByRole('menuitem', { name: 'Edit' })).not.toBeInTheDocument()
    expect(screen.queryByRole('menuitem', { name: 'Download' })).not.toBeInTheDocument()
    expect(screen.getByRole('menuitem', { name: 'Rename' })).toBeInTheDocument()
    expect(screen.getByRole('menuitem', { name: 'Delete' })).toBeInTheDocument()
  })

  it('closes on an outside mousedown', async () => {
    await renderExpandedPanel()

    fireEvent.contextMenu(row('readme.md'))
    expect(screen.getByRole('menu')).toBeInTheDocument()
    expect(document.body.querySelector('[data-testid="sftp-context-menu"]')).not.toBeNull()
    expect(document.querySelector('.sftp-panel [data-testid="sftp-context-menu"]')).toBeNull()

    fireEvent.mouseDown(document.body)
    expect(screen.queryByRole('menu')).not.toBeInTheDocument()
  })
})

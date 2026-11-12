// @vitest-environment jsdom
import { act, fireEvent, screen, waitFor } from '@testing-library/react'
import { describe, expect, it, vi } from 'vitest'
import { SftpPanel } from '../../src/renderer/src/features/sftp'
import { deferred } from '../support/deferred'
import { emitTransferEvent, installFakeApi, renderWithI18n } from '../support/helpers'

function directory(name: string): {
  name: string
  path: string
  isDirectory: boolean
  size: number
  modifyTime: number
} {
  return { name, path: `/home/${name}`, isDirectory: true, size: 0, modifyTime: 0 }
}

function panel(sessionId: string, connected = true, expanded = true): React.JSX.Element {
  return (
    <SftpPanel sessionId={sessionId} connected={connected} expanded={expanded} onToggle={vi.fn()} />
  )
}

describe('SFTP session and directory lifetime', () => {
  it('reconciles backend cwd when a later queued navigation fails after an earlier one succeeds', async () => {
    const fake = installFakeApi()
    fake.mocks.sftp.cwd.mockResolvedValue('/home')
    fake.mocks.sftp.list.mockResolvedValue([directory('one'), directory('two')])
    const first = deferred<string>()
    fake.mocks.sftp.chdir
      .mockReturnValueOnce(first.promise)
      .mockRejectedValueOnce(new Error('missing directory'))
    renderWithI18n(panel('s1'))
    fireEvent.doubleClick(await screen.findByText('one'))
    await waitFor(() => expect(fake.mocks.sftp.chdir).toHaveBeenCalledTimes(1))
    fireEvent.doubleClick(screen.getByText('two'))
    fake.mocks.sftp.cwd.mockResolvedValue('/home/one')
    fake.mocks.sftp.list.mockResolvedValue([directory('actual')])
    await act(async () => first.resolve('/home/one'))
    expect(await screen.findByText('actual')).toBeInTheDocument()
    expect(screen.getByText('missing directory')).toBeInTheDocument()
    expect(screen.getByLabelText('Directory')).toHaveValue('/home/one')
  })

  it('ignores an old listing when switching sessions while collapsed', async () => {
    const fake = installFakeApi()
    const old = deferred<ReturnType<typeof directory>[]>()
    fake.mocks.sftp.cwd.mockImplementation(async (id) => (id === 's1' ? '/old' : '/new'))
    fake.mocks.sftp.list.mockReturnValueOnce(old.promise).mockResolvedValue([directory('new')])
    const view = renderWithI18n(panel('s1'))
    await waitFor(() => expect(fake.mocks.sftp.list).toHaveBeenCalledWith('s1'))
    view.rerender(panel('s2', true, false))
    await act(async () => old.resolve([directory('old')]))
    expect(screen.queryByText('old')).not.toBeInTheDocument()
    expect(fake.mocks.sftp.list).toHaveBeenCalledTimes(1)
    view.rerender(panel('s2'))
    expect(await screen.findByText('new')).toBeInTheDocument()
    expect(screen.getByLabelText('Directory')).toHaveValue('/new')
  })

  it('invalidates a failed initial read across disconnect and reconnect of the same session', async () => {
    const fake = installFakeApi()
    const old = deferred<ReturnType<typeof directory>[]>()
    fake.mocks.sftp.cwd.mockResolvedValue('/home')
    fake.mocks.sftp.list.mockReturnValueOnce(old.promise).mockResolvedValue([directory('current')])
    const view = renderWithI18n(panel('s1'))
    await waitFor(() => expect(fake.mocks.sftp.list).toHaveBeenCalledTimes(1))
    view.rerender(panel('s1', false))
    await act(async () => old.reject(new Error('old read failed')))
    view.rerender(panel('s1'))
    expect(await screen.findByText('current')).toBeInTheDocument()
    expect(screen.queryByText('old read failed')).not.toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Refresh' })).toBeEnabled()
  })

  it('does not send the listing follow-up after a navigation owner is disposed', async () => {
    const fake = installFakeApi()
    fake.mocks.sftp.cwd.mockResolvedValue('/home')
    fake.mocks.sftp.list.mockResolvedValue([directory('docs')])
    const navigation = deferred<string>()
    fake.mocks.sftp.chdir.mockReturnValue(navigation.promise)
    const view = renderWithI18n(panel('s1'))
    fireEvent.doubleClick(await screen.findByText('docs'))
    await waitFor(() => expect(fake.mocks.sftp.chdir).toHaveBeenCalledWith('s1', '/home/docs'))
    const calls = fake.mocks.sftp.list.mock.calls.length
    view.unmount()
    await act(async () => navigation.resolve('/home/docs'))
    expect(fake.mocks.sftp.list).toHaveBeenCalledTimes(calls)
  })

  it('serializes navigation and refresh, keeping the last requested absolute directory', async () => {
    const fake = installFakeApi()
    fake.mocks.sftp.cwd.mockResolvedValue('/home')
    fake.mocks.sftp.list.mockResolvedValue([directory('one'), directory('two')])
    const first = deferred<string>()
    fake.mocks.sftp.chdir.mockReturnValueOnce(first.promise).mockResolvedValue('/home/two')
    renderWithI18n(panel('s1'))
    fireEvent.doubleClick(await screen.findByText('one'))
    await waitFor(() => expect(fake.mocks.sftp.chdir).toHaveBeenCalledTimes(1))
    fireEvent.doubleClick(screen.getByText('two'))
    expect(fake.mocks.sftp.chdir).toHaveBeenCalledTimes(1)
    fake.mocks.sftp.list.mockResolvedValue([directory('latest')])
    await act(async () => first.resolve('/home/one'))
    expect(await screen.findByText('latest')).toBeInTheDocument()
    expect(fake.mocks.sftp.chdir.mock.calls).toEqual([
      ['s1', '/home/one'],
      ['s1', '/home/two']
    ])
    expect(screen.getByLabelText('Directory')).toHaveValue('/home/two')
    expect(fake.mocks.sftp.list).toHaveBeenCalledTimes(2)
  })

  it('clears an open delete confirmation on a session change', async () => {
    const fake = installFakeApi()
    fake.mocks.sftp.cwd.mockResolvedValue('/home')
    fake.mocks.sftp.list.mockResolvedValue([directory('docs')])
    const view = renderWithI18n(panel('s1'))
    fireEvent.contextMenu((await screen.findByText('docs')).closest('li')!)
    fireEvent.click(screen.getByRole('menuitem', { name: 'Delete' }))
    expect(screen.getByRole('alertdialog')).toBeInTheDocument()
    view.rerender(panel('s2'))
    expect(screen.queryByRole('alertdialog')).not.toBeInTheDocument()
    expect(fake.mocks.sftp.remove).not.toHaveBeenCalled()
  })

  it('waits for an in-flight chdir before a transfer completion refresh reads cwd and entries', async () => {
    const fake = installFakeApi()
    fake.mocks.sftp.cwd.mockResolvedValue('/home')
    fake.mocks.sftp.list.mockResolvedValue([directory('docs')])
    const navigation = deferred<string>()
    fake.mocks.sftp.chdir.mockReturnValue(navigation.promise)
    renderWithI18n(panel('s1'))
    fireEvent.doubleClick(await screen.findByText('docs'))
    await waitFor(() => expect(fake.mocks.sftp.chdir).toHaveBeenCalledTimes(1))
    act(() =>
      emitTransferEvent(fake, {
        taskId: 'upload',
        sessionId: 's1',
        sessionTitle: 'Session',
        direction: 'upload',
        name: 'file',
        remotePath: '/home',
        transferred: 1,
        total: 1,
        state: 'succeeded',
        createdAt: 0
      })
    )
    expect(fake.mocks.sftp.cwd).toHaveBeenCalledTimes(1)
    fake.mocks.sftp.cwd.mockResolvedValue('/home/docs')
    fake.mocks.sftp.list.mockResolvedValue([directory('uploaded')])
    await act(async () => navigation.resolve('/home/docs'))
    expect(await screen.findByText('uploaded')).toBeInTheDocument()
    expect(screen.getByLabelText('Directory')).toHaveValue('/home/docs')
    expect(fake.mocks.sftp.list).toHaveBeenCalledTimes(2)
  })

  it('does not refresh the old session after a file command completes in a new one', async () => {
    const fake = installFakeApi()
    fake.mocks.sftp.cwd.mockResolvedValue('/home')
    fake.mocks.sftp.list.mockResolvedValue([directory('docs')])
    const mutation = deferred<void>()
    fake.mocks.sftp.mkdir.mockReturnValue(mutation.promise)
    const prompt = vi.spyOn(window, 'prompt').mockReturnValue('created')
    const view = renderWithI18n(panel('s1'))
    await screen.findByText('docs')
    fireEvent.click(screen.getByRole('button', { name: 'New folder' }))
    expect(fake.mocks.sftp.mkdir).toHaveBeenCalledWith('s1', '/home/created')
    view.rerender(panel('s2'))
    await waitFor(() => expect(fake.mocks.sftp.list).toHaveBeenCalledWith('s2'))
    await act(async () => mutation.resolve(undefined))
    expect(fake.mocks.sftp.list.mock.calls).toEqual([['s1'], ['s2']])
    prompt.mockRestore()
  })
})

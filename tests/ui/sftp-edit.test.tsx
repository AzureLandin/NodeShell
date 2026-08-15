// @vitest-environment jsdom
import { describe, expect, it, vi } from 'vitest'
import { act, fireEvent, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { SftpPanel } from '../../src/renderer/src/components/SftpPanel'
import { SftpTextEditorModal } from '../../src/renderer/src/components/SftpTextEditorModal'
import { MAX_EDITABLE_TEXT_BYTES } from '../../src/shared/editable-text'
import { installFakeApi, renderWithI18n } from './helpers'

// Keep modal tests focused on load/save/dirty; CodeMirror is covered lightly
// via language unit tests and would need a heavier DOM harness here.
vi.mock('../../src/renderer/src/components/SftpCodeEditor', () => ({
  SftpCodeEditor: ({
    initialValue,
    onChange,
    'aria-label': ariaLabel,
    readOnly
  }: {
    initialValue: string
    onChange: (value: string) => void
    'aria-label'?: string
    readOnly?: boolean
  }) => (
    <textarea
      aria-label={ariaLabel}
      defaultValue={initialValue}
      disabled={readOnly}
      onChange={(e) => onChange(e.target.value)}
    />
  )
}))

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
    },
    {
      name: 'photo.png',
      path: '/home/user/photo.png',
      isDirectory: false,
      size: 4096,
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
  await screen.findByText('readme.md')
  return { fake, ...utils }
}

describe('SftpPanel text editing', () => {
  it('shows Edit in the context menu for any file, and opens the editor on double-click for text', async () => {
    const { fake } = await renderExpandedPanel()
    fake.mocks.sftp.readText.mockResolvedValue({
      path: '/home/user/readme.md',
      content: 'hello'
    })

    fireEvent.contextMenu(screen.getByText('readme.md').closest('li') as HTMLElement)
    expect(screen.getByRole('menuitem', { name: 'Edit' })).toBeInTheDocument()

    fireEvent.doubleClick(screen.getByText('readme.md'))

    await waitFor(() =>
      expect(fake.mocks.sftp.readText).toHaveBeenCalledWith('s1', 'readme.md')
    )
    expect(await screen.findByDisplayValue('hello')).toBeInTheDocument()
    expect(fake.mocks.sftp.download).not.toHaveBeenCalled()
    expect(document.body.querySelector('.sftp-editor-modal')).not.toBeNull()
    expect(document.querySelector('.sftp-panel .sftp-editor-modal')).toBeNull()
  })

  it('downloads non-text files on double-click', async () => {
    const { fake } = await renderExpandedPanel()

    fireEvent.doubleClick(screen.getByText('photo.png'))

    await waitFor(() =>
      expect(fake.mocks.sftp.download).toHaveBeenCalledWith('s1', 'photo.png', 'photo.png')
    )
    expect(fake.mocks.sftp.readText).not.toHaveBeenCalled()
  })

  it('opens the editor from the context menu for any file format', async () => {
    const { fake } = await renderExpandedPanel()
    fake.mocks.sftp.readText.mockResolvedValue({
      path: '/home/user/photo.png',
      content: 'PNG'
    })

    fireEvent.contextMenu(screen.getByText('photo.png').closest('li') as HTMLElement)
    expect(screen.getByRole('menuitem', { name: 'Edit' })).toBeInTheDocument()
    fireEvent.click(screen.getByRole('menuitem', { name: 'Edit' }))

    await waitFor(() =>
      expect(fake.mocks.sftp.readText).toHaveBeenCalledWith('s1', 'photo.png')
    )
    expect(await screen.findByDisplayValue('PNG')).toBeInTheDocument()
  })

  it('refuses to open files over the 512KiB GUI cap', async () => {
    const fake = installFakeApi()
    fake.mocks.sftp.cwd.mockResolvedValue('/home/user')
    fake.mocks.sftp.list.mockResolvedValue([
      {
        name: 'huge.json',
        path: '/home/user/huge.json',
        isDirectory: false,
        size: MAX_EDITABLE_TEXT_BYTES + 1,
        modifyTime: 0
      }
    ])
    renderWithI18n(<SftpPanel sessionId="s1" connected expanded onToggle={vi.fn()} />)
    await screen.findByText('huge.json')

    fireEvent.doubleClick(screen.getByText('huge.json'))

    expect(
      await screen.findByText('File is too large to edit in the app (max 512 KB)')
    ).toBeInTheDocument()
    expect(fake.mocks.sftp.readText).not.toHaveBeenCalled()
  })
})

describe('SftpTextEditorModal', () => {
  it('saves dirty content and clears the dirty marker', async () => {
    const fake = installFakeApi()
    fake.mocks.sftp.readText.mockResolvedValue({
      path: '/home/user/a.py',
      content: 'print(1)'
    })
    fake.mocks.sftp.writeText.mockResolvedValue({ path: '/home/user/a.py' })
    const onClose = vi.fn()
    const user = userEvent.setup()

    renderWithI18n(
      <SftpTextEditorModal
        sessionId="s1"
        target={{ name: 'a.py', remotePath: 'a.py' }}
        onClose={onClose}
      />
    )

    const textarea = await screen.findByDisplayValue('print(1)')
    fireEvent.change(textarea, { target: { value: 'print(2)' } })

    expect(screen.getByRole('heading', { name: /Edit a\.py \*/ })).toBeInTheDocument()

    await user.click(screen.getByRole('button', { name: 'Save' }))

    await waitFor(() =>
      expect(fake.mocks.sftp.writeText).toHaveBeenCalledWith('s1', 'a.py', 'print(2)')
    )
    await waitFor(() =>
      expect(screen.getByRole('heading', { name: 'Edit a.py' })).toBeInTheDocument()
    )
    expect(onClose).not.toHaveBeenCalled()
  })

  it('prompts before discarding unsaved changes', async () => {
    const fake = installFakeApi()
    fake.mocks.sftp.readText.mockResolvedValue({
      path: '/home/user/a.py',
      content: 'x'
    })
    const onClose = vi.fn()
    const user = userEvent.setup()

    renderWithI18n(
      <SftpTextEditorModal
        sessionId="s1"
        target={{ name: 'a.py', remotePath: 'a.py' }}
        onClose={onClose}
      />
    )

    const textarea = await screen.findByDisplayValue('x')
    await user.type(textarea, 'y')

    await user.click(screen.getByRole('button', { name: 'Cancel' }))

    expect(
      await screen.findByText('You have unsaved changes. Close without saving?')
    ).toBeInTheDocument()
    expect(onClose).not.toHaveBeenCalled()

    await user.click(screen.getByRole('button', { name: 'Discard' }))

    await waitFor(() => expect(onClose).toHaveBeenCalledTimes(1))
  })

  it('saves with Ctrl/Meta+S', async () => {
    const fake = installFakeApi()
    fake.mocks.sftp.readText.mockResolvedValue({
      path: '/home/user/a.py',
      content: 'a'
    })
    fake.mocks.sftp.writeText.mockResolvedValue({ path: '/home/user/a.py' })
    const user = userEvent.setup()

    renderWithI18n(
      <SftpTextEditorModal
        sessionId="s1"
        target={{ name: 'a.py', remotePath: 'a.py' }}
        onClose={vi.fn()}
      />
    )

    const textarea = await screen.findByDisplayValue('a')
    await user.type(textarea, 'b')
    await act(async () => {
      fireEvent.keyDown(window, { key: 's', ctrlKey: true })
    })

    await waitFor(() =>
      expect(fake.mocks.sftp.writeText).toHaveBeenCalledWith('s1', 'a.py', 'ab')
    )
  })
})

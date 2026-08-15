// @vitest-environment jsdom
import { useState, type ReactElement } from 'react'
import { describe, expect, it, vi } from 'vitest'
import { act, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { AgentPanel } from '../../src/renderer/src/components/AgentPanel'
import type { PermissionAskEvent } from '../../src/shared/types'
import { emitAgentEvent, installFakeApi, renderWithI18n, type FakeApi } from './helpers'

/**
 * Right-hand agent surface: prompts go to the active session, streamed deltas
 * and tool cards render as they arrive, stop/clear reach the backend, and an
 * unconfigured agent points at settings instead of accepting input.
 */

function renderPanel(
  overrides: {
    sessionId?: string | null
    connected?: boolean
    configured?: boolean
    onHide?: ReturnType<typeof vi.fn>
  } = {}
): { fake: FakeApi; onOpenSettings: ReturnType<typeof vi.fn>; onHide?: ReturnType<typeof vi.fn> } {
  const fake = installFakeApi()
  if (overrides.configured === false) {
    fake.mocks.agent.status.mockResolvedValue({
      configured: false,
      providers: [],
      defaultProviderId: '',
      defaultModel: ''
    })
  }
  const onOpenSettings = vi.fn()
  renderWithI18n(
    <AgentPanel
      activeSessionId={overrides.sessionId === undefined ? 's1' : overrides.sessionId}
      activeSessionTitle="prod-web"
      connected={overrides.connected ?? true}
      onOpenSettings={onOpenSettings}
      onHide={overrides.onHide}
    />
  )
  return { fake, onOpenSettings, onHide: overrides.onHide }
}

async function composer(): Promise<HTMLTextAreaElement> {
  return (await screen.findByLabelText('Agent')) as HTMLTextAreaElement
}

describe('AgentPanel prompting', () => {
  it('sends the prompt with the active session and its title, then renders streamed text', async () => {
    const { fake } = renderPanel()
    const user = userEvent.setup()

    await user.type(await composer(), 'how is the disk?')
    await user.click(screen.getByRole('button', { name: 'Send' }))

    await waitFor(() =>
      expect(fake.mocks.agent.prompt).toHaveBeenCalledWith(
        's1',
        'prod-web',
        'how is the disk?',
        'p1',
        'gpt-4o-mini'
      )
    )
    expect(await screen.findByText('how is the disk?')).toBeInTheDocument()

    // Fragments of one turn merge into a single assistant message.
    act(() => {
      emitAgentEvent(fake, 'delta', { sessionId: 's1', delta: 'Disk ' })
      emitAgentEvent(fake, 'delta', { sessionId: 's1', delta: 'looks fine.' })
    })
    expect(await screen.findByText('Disk looks fine.')).toBeInTheDocument()
  })

  it('renders streamed assistant markdown (not the raw markers)', async () => {
    const { fake } = renderPanel()
    await composer()

    act(() => {
      emitAgentEvent(fake, 'delta', { sessionId: 's1', delta: 'Looks **fine** on `/`.\n' })
      emitAgentEvent(fake, 'delta', { sessionId: 's1', delta: '\n```sh\ndf -h\n```' })
    })

    expect(await screen.findByText('fine')).toBeInTheDocument()
    expect(screen.getByText('fine').closest('strong')).toBeInTheDocument()
    expect(screen.getByText('df -h').closest('code')).toBeInTheDocument()
    expect(screen.queryByText('Looks **fine** on `/`.')).not.toBeInTheDocument()
  })

  it('keeps the user message as typed text, not markdown', async () => {
    renderPanel()
    const user = userEvent.setup()

    await user.type(await composer(), 'use **bold** please')
    await user.click(screen.getByRole('button', { name: 'Send' }))

    expect(await screen.findByText('use **bold** please')).toBeInTheDocument()
    const userBubble = document.querySelector('.agent-msg.is-user')
    expect(userBubble?.querySelector('strong')).toBeNull()
  })

  it('renders tool calls and marks failures', async () => {
    const { fake } = renderPanel()
    await composer()

    act(() => {
      emitAgentEvent(fake, 'tool', {
        sessionId: 's1',
        callId: 'c1',
        name: 'bash',
        summary: 'df -h',
        ok: true
      })
      emitAgentEvent(fake, 'tool', {
        sessionId: 's1',
        callId: 'c2',
        name: 'sftp_read',
        summary: '/root/secret',
        ok: false,
        detail: 'permission denied'
      })
    })

    expect(await screen.findByText('Ran')).toBeInTheDocument()
    expect(screen.getByText('df -h')).toBeInTheDocument()
    // Failures start expanded so the path and error are in the nested block.
    expect(screen.getByText('Read')).toBeInTheDocument()
    expect(screen.getByText('/root/secret')).toBeInTheDocument()
    expect(screen.getByText('permission denied')).toBeInTheDocument()
    expect(document.querySelector('.agent-tool.is-failed')).not.toBeNull()
  })

  it('expands a successful tool call into a command block', async () => {
    const { fake } = renderPanel()
    const user = userEvent.setup()
    await composer()

    act(() => {
      emitAgentEvent(fake, 'tool', {
        sessionId: 's1',
        callId: 'c1',
        name: 'bash',
        summary: 'df -h',
        ok: true
      })
    })

    await user.click(await screen.findByRole('button', { name: /Ran df -h/ }))
    expect(await screen.findByText('$ df -h')).toBeInTheDocument()
  })

  it('shows a stop button while running and aborts through the API', async () => {
    const { fake } = renderPanel()
    const user = userEvent.setup()

    await user.type(await composer(), 'tail the log')
    await user.click(screen.getByRole('button', { name: 'Send' }))

    const stop = await screen.findByRole('button', { name: 'Stop' })
    await user.click(stop)
    expect(fake.mocks.agent.abort).toHaveBeenCalledWith('s1')

    // The run only ends on done, which also reports the abort to the user.
    act(() => emitAgentEvent(fake, 'done', { sessionId: 's1', aborted: true }))
    expect(await screen.findByText('Stopped')).toBeInTheDocument()
    expect(await screen.findByRole('button', { name: 'Send' })).toBeInTheDocument()
  })

  it('renders a backend error and reopens the composer', async () => {
    const { fake } = renderPanel()
    const user = userEvent.setup()

    await user.type(await composer(), 'break it')
    await user.click(screen.getByRole('button', { name: 'Send' }))
    act(() => {
      emitAgentEvent(fake, 'error', {
        sessionId: 's1',
        error: { code: 'TIMEOUT', message: 'Agent request timed out' }
      })
      emitAgentEvent(fake, 'done', { sessionId: 's1', aborted: false })
    })

    expect(await screen.findByText('Agent request timed out')).toBeInTheDocument()
    expect(await screen.findByRole('button', { name: 'Send' })).toBeInTheDocument()
  })

  it('clears the transcript through the API', async () => {
    const { fake } = renderPanel()
    const user = userEvent.setup()

    await user.type(await composer(), 'hello')
    await user.click(screen.getByRole('button', { name: 'Send' }))
    expect(await screen.findByText('hello')).toBeInTheDocument()

    await user.click(screen.getByRole('button', { name: 'Agent menu' }))
    await user.click(screen.getByRole('menuitem', { name: 'Clear' }))
    expect(fake.mocks.agent.clear).toHaveBeenCalledWith('s1')
    await waitFor(() => expect(screen.queryByText('hello')).not.toBeInTheDocument())

    act(() => {
      emitAgentEvent(fake, 'delta', { sessionId: 's1', delta: 'stale answer' })
      emitAgentEvent(fake, 'tool', {
        sessionId: 's1',
        callId: 'c1',
        name: 'bash',
        summary: 'uptime',
        ok: true
      })
      emitAgentEvent(fake, 'done', { sessionId: 's1', aborted: true })
    })
    expect(screen.queryByText('stale answer')).not.toBeInTheDocument()
    expect(screen.queryByText('uptime')).not.toBeInTheDocument()
    expect(screen.queryByText('Stopped')).not.toBeInTheDocument()
  })

  it('ignores events addressed to another session', async () => {
    const { fake } = renderPanel()
    await composer()

    act(() => emitAgentEvent(fake, 'delta', { sessionId: 'other', delta: 'not mine' }))

    await waitFor(() => expect(screen.queryByText('not mine')).not.toBeInTheDocument())
  })
})

describe('AgentPanel guards', () => {
  it('requires a connected session', async () => {
    renderPanel({ sessionId: null })
    expect(await screen.findByText('Connect a session to use the agent.')).toBeInTheDocument()
    expect((await composer()).disabled).toBe(true)
  })

  it('shows empty copy when the transcript is idle', async () => {
    renderPanel()
    expect(
      await screen.findByText(
        'Ask about this host. The agent runs commands and reads files over the current SSH session.'
      )
    ).toBeInTheDocument()
  })

  it('hides from the overflow menu when onHide is provided', async () => {
    const onHide = vi.fn()
    renderPanel({ onHide })
    const user = userEvent.setup()

    await user.click(screen.getByRole('button', { name: 'Agent menu' }))
    await user.click(screen.getByRole('menuitem', { name: 'Hide Agent' }))
    expect(onHide).toHaveBeenCalledTimes(1)
  })

  it('points at settings when no API key is stored and opens settings from the notice', async () => {
    const { fake, onOpenSettings } = renderPanel({ configured: false })
    const user = userEvent.setup()

    expect(
      await screen.findByText('Add an API key in Settings to use the agent.')
    ).toBeInTheDocument()
    expect(fake.mocks.agent.prompt).not.toHaveBeenCalled()

    const notice = document.querySelector('.agent-notice') as HTMLElement
    await user.click(within(notice).getByRole('button', { name: 'Settings' }))
    expect(onOpenSettings).toHaveBeenCalled()
  })

  // The status is only re-read on send, so the composer must stay usable: a key
  // added in settings has to take effect without remounting the panel.
  it('keeps the composer enabled while unconfigured and reports the rejection', async () => {
    const { fake } = renderPanel({ configured: false })
    fake.mocks.agent.prompt.mockRejectedValue(
      new Error('NODESHELL_ERR:UNKNOWN:Agent is not configured')
    )
    const user = userEvent.setup()

    const input = await composer()
    expect(input.disabled).toBe(false)
    await user.type(input, 'hello')
    await user.click(screen.getByRole('button', { name: 'Send' }))

    expect(fake.mocks.agent.prompt).not.toHaveBeenCalled()
    expect(
      await screen.findAllByText('Add an API key in Settings to use the agent.')
    ).not.toHaveLength(0)
  })

  it('surfaces a rejected prompt as a message instead of a silent drop', async () => {
    const { fake } = renderPanel()
    fake.mocks.agent.prompt.mockRejectedValue(
      new Error('NODESHELL_ERR:UNKNOWN:Agent is still working on the previous message')
    )
    const user = userEvent.setup()

    await user.type(await composer(), 'again')
    await user.click(screen.getByRole('button', { name: 'Send' }))

    expect(
      await screen.findByText('Agent is still working on the previous message')
    ).toBeInTheDocument()
    expect(await screen.findByRole('button', { name: 'Send' })).toBeInTheDocument()
  })

  it('sends with the model selected in the picker and remembers it as the default', async () => {
    const { fake } = renderPanel()
    const user = userEvent.setup()

    await user.click(await screen.findByLabelText('Model'))
    await user.click(await screen.findByRole('option', { name: 'gpt-4o' }))
    await waitFor(() => expect(fake.mocks.agent.setDefaultModel).toHaveBeenCalledWith('p1', 'gpt-4o'))

    await user.type(await composer(), 'use the bigger model')
    await user.click(screen.getByRole('button', { name: 'Send' }))
    await waitFor(() =>
      expect(fake.mocks.agent.prompt).toHaveBeenCalledWith(
        's1',
        'prod-web',
        'use the bigger model',
        'p1',
        'gpt-4o'
      )
    )
  })

  it('keeps a per-tab model pick when switching sessions', async () => {
    const fake = installFakeApi()
    function Harness(): ReactElement {
      const [id, setId] = useState('s1')
      return (
        <div>
          <button type="button" onClick={() => setId(id === 's1' ? 's2' : 's1')}>
            swap
          </button>
          <AgentPanel
            activeSessionId={id}
            activeSessionTitle="prod-web"
            connected
            onOpenSettings={() => undefined}
          />
        </div>
      )
    }
    renderWithI18n(<Harness />)
    const user = userEvent.setup()

    await user.click(await screen.findByLabelText('Model'))
    await user.click(await screen.findByRole('option', { name: 'gpt-4o' }))
    await user.click(screen.getByRole('button', { name: 'swap' }))
    await user.type(await composer(), 'on tab two')
    await user.click(screen.getByRole('button', { name: 'Send' }))
    await waitFor(() =>
      expect(fake.mocks.agent.prompt).toHaveBeenCalledWith(
        's2',
        'prod-web',
        'on tab two',
        'p1',
        'gpt-4o-mini'
      )
    )

    await user.click(screen.getByRole('button', { name: 'swap' }))
    await user.type(await composer(), 'back on tab one')
    await user.click(screen.getByRole('button', { name: 'Send' }))
    await waitFor(() =>
      expect(fake.mocks.agent.prompt).toHaveBeenCalledWith(
        's1',
        'prod-web',
        'back on tab one',
        'p1',
        'gpt-4o'
      )
    )
  })
})

describe('AgentPanel permission prompt', () => {
  it('renders an inline ask above the composer and reports the decision', async () => {
    installFakeApi()
    const onDecide = vi.fn()
    const request: PermissionAskEvent = {
      id: 'ask-1',
      source: 'agent',
      tool: 'bash',
      sessionId: 's1',
      title: 'prod-web',
      summary: 'uptime'
    }
    renderWithI18n(
      <AgentPanel
        activeSessionId="s1"
        activeSessionTitle="prod-web"
        connected
        onOpenSettings={vi.fn()}
        permissionRequest={request}
        onPermissionDecide={onDecide}
      />
    )
    const dialog = await screen.findByRole('dialog', { name: 'Permission required' })
    expect(dialog.closest('.agent-panel')).toBeTruthy()
    expect(screen.getByText('uptime')).toBeInTheDocument()

    const user = userEvent.setup()
    await user.click(screen.getByRole('button', { name: 'Allow once' }))
    expect(onDecide).toHaveBeenCalledWith('allow')
  })
})

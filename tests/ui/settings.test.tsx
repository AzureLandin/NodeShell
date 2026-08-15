// @vitest-environment jsdom
import { describe, expect, it, vi } from 'vitest'
import { fireEvent, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import type { ComponentProps } from 'react'
import { SettingsModal } from '../../src/renderer/src/components/SettingsModal'
import { installFakeApi, renderWithI18n } from './helpers'
import type { LanguageCode, ThemePreference } from '../../src/shared/types'

/**
 * Settings surface (T1.8.3 / S2.1): font list loading from the API, change
 * callbacks that persist via the parent, and MCP registration going through
 * the API with no Node relay command surfaced in the UI.
 */

function makeProps(): ComponentProps<typeof SettingsModal> {
  return {
    language: 'zh' as LanguageCode,
    themePreference: 'system' as ThemePreference,
    terminalFontFamily: 'Hack',
    terminalFontSize: 14,
    mcpIdleTimeoutMinutes: 10,
    mcpMaxSessions: 8,
    permissionPolicy: 'ask' as const,
    onLanguageChange: vi.fn(),
    onThemePreferenceChange: vi.fn(),
    onTerminalFontFamilyChange: vi.fn(),
    onTerminalFontSizeChange: vi.fn(),
    onMcpIdleTimeoutMinutesChange: vi.fn(),
    onMcpMaxSessionsChange: vi.fn(),
    onPermissionPolicyChange: vi.fn(),
    onClose: vi.fn()
  }
}

async function openCategory(
  user: ReturnType<typeof userEvent.setup>,
  name: 'General' | 'Agent' | 'MCP'
): Promise<void> {
  await user.click(screen.getByRole('button', { name }))
}

async function expandProvider(
  user: ReturnType<typeof userEvent.setup>,
  name: string
): Promise<void> {
  await user.click(screen.getByRole('button', { name }))
}

describe('SettingsModal navigation', () => {
  it('index lists only the three category rows', () => {
    const fake = installFakeApi()
    renderWithI18n(<SettingsModal {...makeProps()} />)

    expect(screen.getAllByRole('button', { name: /^(General|Agent|MCP)$/ })).toHaveLength(3)
    expect(screen.queryByLabelText('Font')).not.toBeInTheDocument()
    expect(screen.queryByRole('button', { name: 'Register' })).not.toBeInTheDocument()
    expect(fake.mocks.fonts.list).not.toHaveBeenCalled()
    expect(fake.mocks.mcpRegistration.status).not.toHaveBeenCalled()
    expect(fake.mocks.agent.status).not.toHaveBeenCalled()
  })
})

describe('SettingsModal language/theme/font', () => {
  it('loads the font list from the API and fires the font change callback on selection', async () => {
    const fake = installFakeApi()
    fake.mocks.fonts.list.mockResolvedValue(['Hack', 'Consolas', 'monospace'])
    const props = makeProps()
    renderWithI18n(<SettingsModal {...props} />)
    const user = userEvent.setup()
    await openCategory(user, 'General')

    await user.click(screen.getByLabelText('Font'))
    const option = await screen.findByRole('option', { name: 'Consolas' })
    await user.click(option)

    expect(props.onTerminalFontFamilyChange).toHaveBeenCalledWith('Consolas')
    expect(fake.mocks.fonts.list).toHaveBeenCalledTimes(1)
  })

  it('fires onLanguageChange when the language select changes', async () => {
    installFakeApi()
    const props = makeProps()
    renderWithI18n(<SettingsModal {...props} />)
    const user = userEvent.setup()
    await openCategory(user, 'General')

    await user.click(screen.getByLabelText('Language'))
    await user.click(await screen.findByRole('option', { name: 'English' }))

    expect(props.onLanguageChange).toHaveBeenCalledWith('en')
  })

  it('fires onThemePreferenceChange when the theme select changes', async () => {
    installFakeApi()
    const props = makeProps()
    renderWithI18n(<SettingsModal {...props} />)
    const user = userEvent.setup()
    await openCategory(user, 'General')

    await user.click(screen.getByLabelText('Theme'))
    await user.click(await screen.findByRole('option', { name: 'Dark' }))

    expect(props.onThemePreferenceChange).toHaveBeenCalledWith('dark')
  })
})

describe('SettingsModal MCP registration', () => {
  it('renders registration status and registers a single target through the API', async () => {
    const fake = installFakeApi()
    fake.mocks.mcpRegistration.status.mockResolvedValue([
      {
        id: 'cursor',
        label: 'Cursor',
        configPath: '/x/cursor.json',
        registered: false,
        stale: false
      }
    ])
    fake.mocks.mcpRegistration.register.mockResolvedValue([
      { id: 'cursor', ok: true, message: 'ok' }
    ])
    renderWithI18n(<SettingsModal {...makeProps()} />)
    const user = userEvent.setup()
    await openCategory(user, 'MCP')

    expect(await screen.findByText('Cursor')).toBeInTheDocument()
    await user.click(screen.getByRole('button', { name: 'Register' }))

    await waitFor(() => expect(fake.mocks.mcpRegistration.register).toHaveBeenCalledWith('cursor'))
    expect(screen.queryByText(/nodeshell-mcp/i)).not.toBeInTheDocument()
  })

  it('register all passes the "all" target to the API', async () => {
    const fake = installFakeApi()
    fake.mocks.mcpRegistration.register.mockResolvedValue([])
    renderWithI18n(<SettingsModal {...makeProps()} />)
    const user = userEvent.setup()
    await openCategory(user, 'MCP')

    await user.click(screen.getByRole('button', { name: 'Register all' }))

    await waitFor(() => expect(fake.mocks.mcpRegistration.register).toHaveBeenCalledWith('all'))
  })

  it('describes native executable --mcp registration with no Node.js requirement', async () => {
    installFakeApi()
    renderWithI18n(<SettingsModal {...makeProps()} />)
    await openCategory(userEvent.setup(), 'MCP')

    // S3.5: registration writes the native executable path + --mcp; the
    // shipped product must not require a Node.js runtime. The rendered hint
    // is the visible contract, so assert on the translated copy itself.
    const hint = await screen.findByText(
      (_, el) =>
        el?.classList.contains('settings-hint') === true &&
        el.textContent?.includes('--mcp') === true
    )
    expect(hint.textContent).toContain('--mcp')
    expect(hint.textContent).toContain('native')
    expect(hint.textContent).not.toMatch(/node\.js|requires? node/i)
  })

  it('shows the config path on each one-click target', async () => {
    const fake = installFakeApi()
    fake.mocks.mcpRegistration.status.mockResolvedValue([
      {
        id: 'cursor',
        label: 'Cursor',
        configPath: '/x/cursor.json',
        registered: false,
        stale: false
      }
    ])
    renderWithI18n(<SettingsModal {...makeProps()} />)
    await openCategory(userEvent.setup(), 'MCP')
    expect(await screen.findByText('/x/cursor.json')).toBeInTheDocument()
  })

  it('shows the launch command and switches snippet preview by format', async () => {
    const fake = installFakeApi()
    fake.mocks.mcpRegistration.manualConfig.mockResolvedValue({
      command: '/opt/NodeShell',
      args: ['--mcp'],
      snippets: {
        standard: '{"mcpServers":{"nodeshell":{}}}',
        vscode: '{"servers":{"nodeshell":{"type":"stdio"}}}',
        opencode: '{"mcp":{"nodeshell":{"type":"local"}}}',
        codex: '[mcp_servers.nodeshell]'
      }
    })
    renderWithI18n(<SettingsModal {...makeProps()} />)
    const user = userEvent.setup()
    await openCategory(user, 'MCP')

    const launch = await screen.findByLabelText('Launch command')
    expect(launch).toHaveValue('/opt/NodeShell --mcp')

    const preview = screen.getByLabelText('Config preview')
    expect(preview).toHaveValue('{"mcpServers":{"nodeshell":{}}}')

    await user.click(screen.getByRole('tab', { name: 'VS Code' }))
    expect(preview).toHaveValue('{"servers":{"nodeshell":{"type":"stdio"}}}')

    await user.click(screen.getByRole('tab', { name: 'OpenCode' }))
    expect(preview).toHaveValue('{"mcp":{"nodeshell":{"type":"local"}}}')

    await user.click(screen.getByRole('tab', { name: 'Codex' }))
    expect(preview).toHaveValue('[mcp_servers.nodeshell]')
  })

  it('copies the selected snippet format', async () => {
    const writeText = vi.fn().mockResolvedValue(undefined)
    Object.defineProperty(navigator, 'clipboard', {
      value: { writeText },
      configurable: true
    })

    const fake = installFakeApi()
    fake.mocks.mcpRegistration.manualConfig.mockResolvedValue({
      command: '/opt/NodeShell',
      args: ['--mcp'],
      snippets: {
        standard: 'STANDARD',
        vscode: 'VSCODE',
        opencode: 'OPENCODE',
        codex: 'CODEX'
      }
    })
    renderWithI18n(<SettingsModal {...makeProps()} />)
    fireEvent.click(screen.getByRole('button', { name: 'MCP' }))

    await screen.findByLabelText('Config preview')
    fireEvent.click(screen.getByRole('tab', { name: 'VS Code' }))
    await waitFor(() => expect(screen.getByLabelText('Config preview')).toHaveValue('VSCODE'))
    fireEvent.click(screen.getByRole('button', { name: 'Copy config' }))

    await waitFor(() => expect(writeText).toHaveBeenCalledWith('VSCODE'))
    expect(await screen.findByText('Copied to clipboard')).toBeInTheDocument()
  })
})

describe('SettingsModal agent providers', () => {
  it('loads a provider and saves it with the key, then empties the key field', async () => {
    const fake = installFakeApi()
    fake.mocks.agent.status.mockResolvedValue({
      configured: false,
      providers: [
        {
          id: 'p1',
          name: 'OpenAI',
          baseUrl: 'https://api.openai.com/v1',
          models: ['gpt-4o-mini'],
          hasKey: false
        }
      ],
      defaultProviderId: 'p1',
      defaultModel: 'gpt-4o-mini'
    })
    const saved = {
      configured: true,
      providers: [
        {
          id: 'p1',
          name: 'DeepSeek',
          baseUrl: 'https://api.deepseek.com/v1',
          models: ['deepseek-chat'],
          hasKey: true
        }
      ],
      defaultProviderId: 'p1',
      defaultModel: 'deepseek-chat'
    }
    fake.mocks.agent.upsertProvider.mockResolvedValue({ ...saved, configured: false, providers: [{ ...saved.providers[0], hasKey: false }] })
    fake.mocks.agent.setProviderKey.mockResolvedValue(saved)
    renderWithI18n(<SettingsModal {...makeProps()} />)
    const user = userEvent.setup()
    await openCategory(user, 'Agent')
    await expandProvider(user, 'OpenAI')

    const baseUrl = await screen.findByLabelText('Base URL 1')
    expect(baseUrl).toHaveValue('https://api.openai.com/v1')
    const key = screen.getByLabelText('API key 1')
    expect(key).toHaveValue('')
    expect(key).toHaveAttribute('placeholder', 'Not set')

    await user.clear(screen.getByLabelText('Name 1'))
    await user.type(screen.getByLabelText('Name 1'), 'DeepSeek')
    await user.clear(baseUrl)
    await user.type(baseUrl, 'https://api.deepseek.com/v1')
    await user.clear(screen.getByLabelText('Models 1'))
    await user.type(screen.getByLabelText('Models 1'), 'deepseek-chat')
    await user.type(key, 'sk-secret')
    await user.click(screen.getByRole('button', { name: 'Save' }))

    await waitFor(() =>
      expect(fake.mocks.agent.upsertProvider).toHaveBeenCalledWith({
        id: 'p1',
        name: 'DeepSeek',
        baseUrl: 'https://api.deepseek.com/v1',
        models: ['deepseek-chat']
      })
    )
    await waitFor(() => expect(fake.mocks.agent.setProviderKey).toHaveBeenCalledWith('p1', 'sk-secret'))
    await waitFor(() =>
      expect(screen.getByRole('button', { name: 'DeepSeek' })).toHaveAttribute('aria-expanded', 'false')
    )

    await expandProvider(user, 'DeepSeek')
    await waitFor(() => expect(screen.getByLabelText('API key 1')).toHaveValue(''))
    expect(screen.getByLabelText('API key 1')).toHaveAttribute(
      'placeholder',
      'Stored — type to replace'
    )
  })

  it('omits the key when the field was left untouched', async () => {
    const fake = installFakeApi()
    fake.mocks.agent.upsertProvider.mockResolvedValue({
      configured: true,
      providers: [
        {
          id: 'p1',
          name: 'OpenAI',
          baseUrl: 'https://api.openai.com/v1',
          models: ['gpt-4o'],
          hasKey: true
        }
      ],
      defaultProviderId: 'p1',
      defaultModel: 'gpt-4o'
    })
    renderWithI18n(<SettingsModal {...makeProps()} />)
    const user = userEvent.setup()
    await openCategory(user, 'Agent')
    await expandProvider(user, 'OpenAI')

    const models = await screen.findByLabelText('Models 1')
    await waitFor(() => expect(models).toBeEnabled())
    await user.clear(models)
    await user.type(models, 'gpt-4o')
    await user.click(screen.getByRole('button', { name: 'Save' }))

    await waitFor(() =>
      expect(fake.mocks.agent.upsertProvider).toHaveBeenCalledWith({
        id: 'p1',
        name: 'OpenAI',
        baseUrl: 'https://api.openai.com/v1',
        models: ['gpt-4o']
      })
    )
    expect(fake.mocks.agent.setProviderKey).not.toHaveBeenCalled()
  })

  it('clears the stored key through the API', async () => {
    const fake = installFakeApi()
    fake.mocks.agent.setProviderKey.mockResolvedValue({
      configured: false,
      providers: [
        {
          id: 'p1',
          name: 'OpenAI',
          baseUrl: 'https://api.openai.com/v1',
          models: ['gpt-4o-mini'],
          hasKey: false
        }
      ],
      defaultProviderId: 'p1',
      defaultModel: 'gpt-4o-mini'
    })
    renderWithI18n(<SettingsModal {...makeProps()} />)
    const user = userEvent.setup()
    await openCategory(user, 'Agent')
    await expandProvider(user, 'OpenAI')

    await waitFor(() => expect(screen.getByRole('button', { name: 'Clear key' })).toBeEnabled())
    await user.click(screen.getByRole('button', { name: 'Clear key' }))

    await waitFor(() => expect(fake.mocks.agent.setProviderKey).toHaveBeenCalledWith('p1', ''))
    expect(await screen.findByText('API key cleared')).toBeInTheDocument()
  })

  it('does not save until the stored providers have loaded', async () => {
    const fake = installFakeApi()
    let release!: (value: {
      configured: boolean
      providers: Array<{
        id: string
        name: string
        baseUrl: string
        models: string[]
        hasKey: boolean
      }>
      defaultProviderId: string
      defaultModel: string
    }) => void
    fake.mocks.agent.status.mockReturnValue(
      new Promise((resolve) => {
        release = resolve
      })
    )
    renderWithI18n(<SettingsModal {...makeProps()} />)
    const user = userEvent.setup()
    await openCategory(user, 'Agent')

    const add = await screen.findByRole('button', { name: 'Add provider' })
    expect(add).toBeDisabled()
    expect(fake.mocks.agent.upsertProvider).not.toHaveBeenCalled()

    release({
      configured: true,
      providers: [
        {
          id: 'p1',
          name: 'DeepSeek',
          baseUrl: 'https://api.deepseek.com/v1',
          models: ['deepseek-chat'],
          hasKey: true
        }
      ],
      defaultProviderId: 'p1',
      defaultModel: 'deepseek-chat'
    })
    await waitFor(() => expect(screen.getByRole('button', { name: 'Add provider' })).toBeEnabled())
    await expandProvider(user, 'DeepSeek')
    await waitFor(() => expect(screen.getByRole('button', { name: 'Save' })).toBeEnabled())
    expect(screen.getByLabelText('Base URL 1')).toHaveValue('https://api.deepseek.com/v1')
    expect(screen.getByLabelText('Models 1')).toHaveValue('deepseek-chat')
  })

  it('adds a card expanded, collapses it after save, and expands again on click', async () => {
    const fake = installFakeApi()
    fake.mocks.agent.status.mockResolvedValue({
      configured: false,
      providers: [],
      defaultProviderId: '',
      defaultModel: ''
    })
    fake.mocks.agent.upsertProvider.mockResolvedValue({
      configured: true,
      providers: [
        {
          id: 'p2',
          name: 'Groq',
          baseUrl: 'https://api.groq.com/openai/v1',
          models: ['llama'],
          hasKey: false
        }
      ],
      defaultProviderId: 'p2',
      defaultModel: 'llama'
    })
    renderWithI18n(<SettingsModal {...makeProps()} />)
    const user = userEvent.setup()
    await openCategory(user, 'Agent')

    await waitFor(() => expect(screen.getByRole('button', { name: 'Add provider' })).toBeEnabled())
    await user.click(screen.getByRole('button', { name: 'Add provider' }))
    expect(screen.getByRole('button', { name: 'New provider' })).toHaveAttribute('aria-expanded', 'true')
    expect(screen.getByLabelText('Name 1')).toBeInTheDocument()

    await user.type(screen.getByLabelText('Name 1'), 'Groq')
    await user.type(screen.getByLabelText('Base URL 1'), 'https://api.groq.com/openai/v1')
    await user.type(screen.getByLabelText('Models 1'), 'llama')
    await user.click(screen.getByRole('button', { name: 'Save' }))

    await waitFor(() =>
      expect(screen.getByRole('button', { name: 'Groq' })).toHaveAttribute('aria-expanded', 'false')
    )
    const collapsed = screen.getByRole('button', { name: 'Groq' })
    expect(collapsed).toHaveAttribute('aria-expanded', 'false')

    await expandProvider(user, 'Groq')
    expect(screen.getByLabelText('Base URL 1')).toHaveValue('https://api.groq.com/openai/v1')
  })
})

describe('SettingsModal permissions', () => {
  it('fires the policy change callback when a new option is selected', async () => {
    installFakeApi()
    const props = makeProps()
    renderWithI18n(<SettingsModal {...props} />)
    const user = userEvent.setup()
    await openCategory(user, 'General')

    await user.click(screen.getByLabelText('Sensitive operations'))
    await user.click(await screen.findByRole('option', { name: 'Deny' }))

    expect(props.onPermissionPolicyChange).toHaveBeenCalledWith('deny')
  })
})

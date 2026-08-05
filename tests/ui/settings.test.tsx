// @vitest-environment jsdom
import { describe, expect, it, vi } from 'vitest'
import { screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import type { ComponentProps } from 'react'
import { SettingsModal } from '../../src/renderer/src/components/SettingsModal'
import { installFakeApi } from './helpers'
import { renderWithI18n } from './helpers'
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
    onLanguageChange: vi.fn(),
    onThemePreferenceChange: vi.fn(),
    onTerminalFontFamilyChange: vi.fn(),
    onTerminalFontSizeChange: vi.fn(),
    onMcpIdleTimeoutMinutesChange: vi.fn(),
    onMcpMaxSessionsChange: vi.fn(),
    onClose: vi.fn()
  }
}

describe('SettingsModal language/theme/font', () => {
  it('loads the font list from the API and fires the font change callback on selection', async () => {
    const fake = installFakeApi()
    fake.mocks.fonts.list.mockResolvedValue(['Hack', 'Consolas', 'monospace'])
    const props = makeProps()
    renderWithI18n(<SettingsModal {...props} />)
    const user = userEvent.setup()

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

    await user.click(screen.getByLabelText('Language'))
    await user.click(await screen.findByRole('option', { name: 'English' }))

    expect(props.onLanguageChange).toHaveBeenCalledWith('en')
  })

  it('fires onThemePreferenceChange when the theme select changes', async () => {
    installFakeApi()
    const props = makeProps()
    renderWithI18n(<SettingsModal {...props} />)
    const user = userEvent.setup()

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

    await user.click(screen.getByRole('button', { name: 'Register all' }))

    await waitFor(() => expect(fake.mocks.mcpRegistration.register).toHaveBeenCalledWith('all'))
  })

  it('describes native executable --mcp registration with no Node.js requirement', async () => {
    installFakeApi()
    renderWithI18n(<SettingsModal {...makeProps()} />)

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
})

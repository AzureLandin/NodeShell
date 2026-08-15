import { useEffect, useState } from 'react'
import { useTranslation } from 'react-i18next'
import type {
  LanguageCode,
  McpManualConfig,
  McpRegistrationTargetStatus,
  McpSnippetFormat,
  PermissionPolicy,
  ThemePreference
} from '../../../shared/types'
import { AboutModal } from './AboutModal'
import { ModalShell, useModalClose } from './ModalShell'
import { Select } from './Select'

const MCP_SNIPPET_FORMATS: McpSnippetFormat[] = ['standard', 'vscode', 'opencode', 'codex']

function formatMcpLaunchCommand(command: string, args: string[]): string {
  const quote = (s: string): string => {
    if (s === '') return '""'
    if (/[\s"]/.test(s)) return `"${s.replace(/"/g, '\\"')}"`
    return s
  }
  return [command, ...args].map(quote).join(' ')
}

interface SettingsModalProps {
  language: LanguageCode
  themePreference: ThemePreference
  terminalFontFamily: string
  terminalFontSize: number
  mcpIdleTimeoutMinutes: number
  mcpMaxSessions: number
  permissionPolicy: PermissionPolicy
  onLanguageChange: (language: LanguageCode) => void
  onThemePreferenceChange: (theme: ThemePreference) => void
  onTerminalFontFamilyChange: (family: string) => void
  onTerminalFontSizeChange: (size: number) => void
  onMcpIdleTimeoutMinutesChange: (minutes: number) => void
  onMcpMaxSessionsChange: (max: number) => void
  onPermissionPolicyChange: (policy: PermissionPolicy) => void
  onClose: () => void
}

/**
 * Agent endpoint settings. The API key is write-only from here: the backend
 * stores it in the OS keyring and reports back only whether one exists, so the
 * renderer never holds the secret and the field starts empty on every open.
 * The section is skipped entirely when the running bridge has no agent.
 */
function AgentSettingsSection(): React.JSX.Element | null {
  const { t } = useTranslation()
  const agent = window.api.agent
  const [baseUrl, setBaseUrl] = useState('')
  const [model, setModel] = useState('')
  const [apiKey, setApiKey] = useState('')
  const [configured, setConfigured] = useState(false)
  const [busy, setBusy] = useState(false)
  const [ready, setReady] = useState(false)
  const [message, setMessage] = useState<string | null>(null)

  useEffect(() => {
    if (!agent) return
    void (async () => {
      try {
        const status = await agent.status()
        setBaseUrl(status.baseUrl)
        setModel(status.model)
        setConfigured(status.configured)
      } catch (err) {
        setMessage(err instanceof Error ? err.message : String(err))
      } finally {
        setReady(true)
      }
    })()
  }, [agent])

  if (!agent) return null

  const save = async (): Promise<void> => {
    setBusy(true)
    setMessage(null)
    try {
      const status = await agent.setConfig({
        baseUrl,
        model,
        // An untouched key field must not clear the stored key.
        ...(apiKey === '' ? {} : { apiKey })
      })
      setBaseUrl(status.baseUrl)
      setModel(status.model)
      setConfigured(status.configured)
      setApiKey('')
      setMessage(t('settings.agentSaved'))
    } catch (err) {
      setMessage(err instanceof Error ? err.message : String(err))
    } finally {
      setBusy(false)
    }
  }

  const clearKey = async (): Promise<void> => {
    setBusy(true)
    setMessage(null)
    try {
      const status = await agent.setConfig({ apiKey: '' })
      setConfigured(status.configured)
      setApiKey('')
      setMessage(t('settings.agentKeyCleared'))
    } catch (err) {
      setMessage(err instanceof Error ? err.message : String(err))
    } finally {
      setBusy(false)
    }
  }

  return (
    <fieldset className="settings-section">
      <legend>{t('settings.agent')}</legend>
      <p className="settings-hint">{t('settings.agentHint')}</p>

      <div className="form-field">
        <span>{t('settings.agentBaseUrl')}</span>
        <input
          type="text"
          value={baseUrl}
          spellCheck={false}
          aria-label={t('settings.agentBaseUrl')}
          disabled={!ready || busy}
          onChange={(e) => setBaseUrl(e.target.value)}
        />
      </div>

      <div className="form-field">
        <span>{t('settings.agentModel')}</span>
        <input
          type="text"
          value={model}
          spellCheck={false}
          aria-label={t('settings.agentModel')}
          disabled={!ready || busy}
          onChange={(e) => setModel(e.target.value)}
        />
      </div>

      <div className="form-field">
        <span>{t('settings.agentApiKey')}</span>
        <input
          type="password"
          value={apiKey}
          autoComplete="off"
          spellCheck={false}
          aria-label={t('settings.agentApiKey')}
          placeholder={configured ? t('settings.agentKeyStored') : t('settings.agentKeyMissing')}
          disabled={!ready || busy}
          onChange={(e) => setApiKey(e.target.value)}
        />
      </div>

      <div className="mcp-register-actions">
        <button
          type="button"
          className="btn-primary btn-sm"
          disabled={!ready || busy}
          onClick={() => void save()}
        >
          {t('settings.agentSave')}
        </button>
        <button
          type="button"
          className="btn-secondary btn-sm"
          disabled={!ready || busy || !configured}
          onClick={() => void clearKey()}
        >
          {t('settings.agentClearKey')}
        </button>
      </div>
      {message && <p className="mcp-register-message">{message}</p>}
    </fieldset>
  )
}

function SettingsModalBody({
  language,
  themePreference,
  terminalFontFamily,
  terminalFontSize,
  mcpIdleTimeoutMinutes,
  mcpMaxSessions,
  permissionPolicy,
  onLanguageChange,
  onThemePreferenceChange,
  onTerminalFontFamilyChange,
  onTerminalFontSizeChange,
  onMcpIdleTimeoutMinutesChange,
  onMcpMaxSessionsChange,
  onPermissionPolicyChange,
  onOpenAbout
}: Omit<SettingsModalProps, 'onClose'> & { onOpenAbout: () => void }): React.JSX.Element {
  const { t } = useTranslation()
  const requestClose = useModalClose()
  const [fonts, setFonts] = useState<string[]>([])
  const [mcpTargets, setMcpTargets] = useState<McpRegistrationTargetStatus[]>([])
  const [mcpManual, setMcpManual] = useState<McpManualConfig | null>(null)
  const [mcpFormat, setMcpFormat] = useState<McpSnippetFormat>('standard')
  const [mcpBusy, setMcpBusy] = useState(false)
  const [mcpMessage, setMcpMessage] = useState<string | null>(null)

  const refreshMcpStatus = async (): Promise<void> => {
    try {
      const rows = await window.api.mcpRegistration.status()
      setMcpTargets(rows)
    } catch (err) {
      setMcpTargets([])
      setMcpMessage(err instanceof Error ? err.message : String(err))
    }
  }

  const refreshMcpManual = async (): Promise<void> => {
    try {
      const kit = await window.api.mcpRegistration.manualConfig()
      setMcpManual(kit)
    } catch (err) {
      setMcpManual(null)
      setMcpMessage(err instanceof Error ? err.message : String(err))
    }
  }

  useEffect(() => {
    void (async () => {
      try {
        const list = await window.api.fonts.list()
        setFonts(list)
      } catch {
        setFonts([])
      }
    })()
    void refreshMcpStatus()
    void refreshMcpManual()
  }, [])

  const registerMcp = async (target: 'all' | McpRegistrationTargetStatus['id']): Promise<void> => {
    setMcpBusy(true)
    setMcpMessage(null)
    try {
      const results = await window.api.mcpRegistration.register(target)
      const failed = results.filter((r) => !r.ok)
      if (failed.length === 0) {
        setMcpMessage(t('settings.mcpRegisterOk'))
      } else {
        setMcpMessage(failed.map((r) => `${r.id}: ${r.message}`).join('; '))
      }
      await refreshMcpStatus()
    } catch (err) {
      setMcpMessage(err instanceof Error ? err.message : String(err))
    } finally {
      setMcpBusy(false)
    }
  }

  const copyText = async (text: string, okKey: string): Promise<void> => {
    setMcpBusy(true)
    setMcpMessage(null)
    try {
      await navigator.clipboard.writeText(text)
      setMcpMessage(t(okKey))
    } catch (err) {
      setMcpMessage(err instanceof Error ? err.message : String(err))
    } finally {
      setMcpBusy(false)
    }
  }

  const copyMcpSnippet = async (): Promise<void> => {
    const text = mcpManual?.snippets[mcpFormat]
    if (!text) return
    await copyText(text, 'settings.mcpCopyOk')
  }

  const copyMcpCommand = async (): Promise<void> => {
    if (!mcpManual) return
    await copyText(formatMcpLaunchCommand(mcpManual.command, mcpManual.args), 'settings.mcpCopyCommandOk')
  }

  const fontOptions =
    fonts.includes(terminalFontFamily) || !terminalFontFamily
      ? fonts
      : [terminalFontFamily, ...fonts]

  const fontSelectOptions =
    fontOptions.length === 0 && terminalFontFamily
      ? [{ value: terminalFontFamily, label: terminalFontFamily }]
      : fontOptions.map((name) => ({ value: name, label: name }))

  const fontSize = Math.min(24, Math.max(10, Math.round(terminalFontSize) || 14))
  const fontSizeOptions = Array.from({ length: 15 }, (_, i) => {
    const size = 10 + i
    return { value: String(size), label: String(size) }
  })

  const idleOptions = [1, 5, 10, 15, 30, 60, 120].map((n) => ({
    value: String(n),
    label: String(n)
  }))
  const maxSessionOptions = [1, 2, 4, 8, 16, 32].map((n) => ({
    value: String(n),
    label: String(n)
  }))

  const idleValue = String(
    [1, 5, 10, 15, 30, 60, 120].includes(mcpIdleTimeoutMinutes)
      ? mcpIdleTimeoutMinutes
      : Math.min(120, Math.max(1, mcpIdleTimeoutMinutes))
  )
  if (!idleOptions.some((o) => o.value === idleValue)) {
    idleOptions.push({ value: idleValue, label: idleValue })
    idleOptions.sort((a, b) => Number(a.value) - Number(b.value))
  }

  const maxValue = String(
    [1, 2, 4, 8, 16, 32].includes(mcpMaxSessions)
      ? mcpMaxSessions
      : Math.min(32, Math.max(1, mcpMaxSessions))
  )
  if (!maxSessionOptions.some((o) => o.value === maxValue)) {
    maxSessionOptions.push({ value: maxValue, label: maxValue })
    maxSessionOptions.sort((a, b) => Number(a.value) - Number(b.value))
  }

  return (
    <>
      <div className="settings-modal-header">
        <h3 id="settings-modal-title" className="modal-title">
          {t('settings.title')}
        </h3>
        <button
          type="button"
          className="settings-modal-close"
          aria-label={t('common.dismiss')}
          onClick={requestClose}
        >
          ×
        </button>
      </div>

      <div className="settings-modules">
        <div className="settings-modules-main">
          <div className="form-field">
            <span>{t('common.language')}</span>
            <Select
              value={language}
              onChange={(v) => onLanguageChange(v as LanguageCode)}
              aria-label={t('common.language')}
              options={[
                { value: 'zh', label: '中文' },
                { value: 'en', label: 'English' }
              ]}
            />
          </div>

          <fieldset className="settings-section">
            <legend>{t('settings.appearance')}</legend>
            <div className="form-field">
              <span>{t('settings.theme')}</span>
              <Select
                value={themePreference}
                onChange={(v) => onThemePreferenceChange(v as ThemePreference)}
                aria-label={t('settings.theme')}
                options={[
                  { value: 'system', label: t('settings.themeSystem') },
                  { value: 'light', label: t('settings.themeLight') },
                  { value: 'dark', label: t('settings.themeDark') }
                ]}
              />
            </div>
          </fieldset>

          <fieldset className="settings-section">
            <legend>{t('settings.terminal')}</legend>

            <div className="form-field">
              <span>{t('settings.fontFamily')}</span>
              <Select
                value={terminalFontFamily}
                onChange={onTerminalFontFamilyChange}
                aria-label={t('settings.fontFamily')}
                options={fontSelectOptions}
              />
            </div>

            <div className="form-field">
              <span>{t('settings.fontSize')}</span>
              <Select
                value={String(fontSize)}
                onChange={(v) => {
                  const n = Number(v)
                  if (!Number.isFinite(n)) return
                  onTerminalFontSizeChange(n)
                }}
                aria-label={t('settings.fontSize')}
                options={fontSizeOptions}
              />
            </div>
          </fieldset>

          <AgentSettingsSection />

          <fieldset className="settings-section">
            <legend>{t('settings.permissions')}</legend>
            <p className="settings-hint">{t('settings.permissionsHint')}</p>
            <div className="form-field">
              <span>{t('settings.permissionPolicy')}</span>
              <Select
                value={permissionPolicy}
                onChange={(v) => onPermissionPolicyChange(v as PermissionPolicy)}
                aria-label={t('settings.permissionPolicy')}
                options={[
                  { value: 'ask', label: t('settings.permissionAsk') },
                  { value: 'allow', label: t('settings.permissionAllow') },
                  { value: 'deny', label: t('settings.permissionDeny') }
                ]}
              />
            </div>
          </fieldset>
        </div>

        <fieldset className="settings-section settings-section--mcp">
          <legend>{t('settings.mcp')}</legend>
          <p className="settings-hint">{t('settings.mcpHint')}</p>

          <div className="settings-mcp-options">
            <div className="form-field">
              <span>{t('settings.mcpIdleTimeout')}</span>
              <Select
                value={idleValue}
                onChange={(v) => {
                  const n = Number(v)
                  if (!Number.isFinite(n)) return
                  onMcpIdleTimeoutMinutesChange(n)
                }}
                aria-label={t('settings.mcpIdleTimeout')}
                options={idleOptions}
              />
            </div>

            <div className="form-field">
              <span>{t('settings.mcpMaxSessions')}</span>
              <Select
                value={maxValue}
                onChange={(v) => {
                  const n = Number(v)
                  if (!Number.isFinite(n)) return
                  onMcpMaxSessionsChange(n)
                }}
                aria-label={t('settings.mcpMaxSessions')}
                options={maxSessionOptions}
              />
            </div>
          </div>

          <div className="mcp-register-block">
            <div className="mcp-register-title">{t('settings.mcpRegisterTitle')}</div>
            <p className="settings-hint">{t('settings.mcpRegisterHint')}</p>
            <div className="mcp-register-actions">
              <button
                type="button"
                className="btn-primary btn-sm"
                disabled={mcpBusy}
                onClick={() => void registerMcp('all')}
              >
                {t('settings.mcpRegisterAll')}
              </button>
            </div>
            <ul className="mcp-register-list">
              {mcpTargets.map((row) => {
                const statusLabel = row.registered
                  ? t('settings.mcpStatusRegistered')
                  : row.stale
                    ? t('settings.mcpStatusStale')
                    : t('settings.mcpStatusMissing')
                return (
                  <li key={row.id} className="mcp-register-row">
                    <div className="mcp-register-meta">
                      <span className="mcp-register-name">{row.label}</span>
                      {row.configPath ? (
                        <span className="mcp-register-path" title={row.configPath}>
                          {row.configPath}
                        </span>
                      ) : null}
                      <span
                        className={`mcp-register-status${row.registered ? ' is-ok' : row.stale ? ' is-stale' : ''}`}
                      >
                        {statusLabel}
                      </span>
                    </div>
                    <button
                      type="button"
                      className="btn-secondary btn-sm"
                      disabled={mcpBusy}
                      onClick={() => void registerMcp(row.id)}
                    >
                      {row.registered ? t('settings.mcpUpdate') : t('settings.mcpRegister')}
                    </button>
                  </li>
                )
              })}
            </ul>
          </div>

          <div className="mcp-register-block">
            <div className="mcp-register-title">{t('settings.mcpOtherTitle')}</div>
            <p className="settings-hint">{t('settings.mcpOtherHint')}</p>
            <div className="mcp-launch-row">
              <label className="mcp-launch-field">
                <span>{t('settings.mcpLaunchCommand')}</span>
                <input
                  type="text"
                  readOnly
                  className="mcp-launch-input"
                  aria-label={t('settings.mcpLaunchCommand')}
                  value={
                    mcpManual
                      ? formatMcpLaunchCommand(mcpManual.command, mcpManual.args)
                      : ''
                  }
                />
              </label>
              <button
                type="button"
                className="btn-secondary btn-sm"
                disabled={mcpBusy || !mcpManual}
                onClick={() => void copyMcpCommand()}
              >
                {t('settings.mcpCopyCommand')}
              </button>
            </div>
            <div
              className="mcp-format-tabs"
              role="tablist"
              aria-label={t('settings.mcpSnippetFormat')}
            >
              {MCP_SNIPPET_FORMATS.map((format) => (
                <button
                  key={format}
                  type="button"
                  role="tab"
                  aria-selected={mcpFormat === format}
                  className={`mcp-format-tab${mcpFormat === format ? ' is-active' : ''}`}
                  onClick={() => setMcpFormat(format)}
                >
                  {t(`settings.mcpFormat.${format}`)}
                </button>
              ))}
            </div>
            <textarea
              className="mcp-snippet-preview"
              readOnly
              spellCheck={false}
              aria-label={t('settings.mcpSnippetPreview')}
              value={mcpManual?.snippets[mcpFormat] ?? ''}
            />
            <div className="mcp-register-actions">
              <button
                type="button"
                className="btn-secondary btn-sm"
                disabled={mcpBusy || !mcpManual?.snippets[mcpFormat]}
                onClick={() => void copyMcpSnippet()}
              >
                {t('settings.mcpCopyConfig')}
              </button>
            </div>
            <div className="mcp-paste-locations">
              <div className="mcp-paste-title">{t('settings.mcpPasteLocations')}</div>
              <ul>
                <li>{t('settings.mcpPasteClaudeDesktop')}</li>
                <li>{t('settings.mcpPasteVscode')}</li>
                <li>{t('settings.mcpPasteWindsurf')}</li>
              </ul>
            </div>
            {mcpMessage && <p className="mcp-register-message">{mcpMessage}</p>}
          </div>
        </fieldset>
      </div>

      <div className="form-actions">
        <button type="button" className="btn-secondary" onClick={onOpenAbout}>
          {t('about.open')}
        </button>
        <button type="button" className="btn-primary" onClick={requestClose}>
          {t('common.dismiss')}
        </button>
      </div>
    </>
  )
}

export function SettingsModal(props: SettingsModalProps): React.JSX.Element {
  const { onClose, ...bodyProps } = props
  const [showAbout, setShowAbout] = useState(false)

  if (showAbout) {
    return (
      <AboutModal
        onClose={onClose}
        onBack={() => setShowAbout(false)}
      />
    )
  }

  return (
    <ModalShell onClose={onClose} dialogClassName="settings-modal" labelledBy="settings-modal-title">
      <SettingsModalBody {...bodyProps} onOpenAbout={() => setShowAbout(true)} />
    </ModalShell>
  )
}

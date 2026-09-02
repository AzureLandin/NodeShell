import { useEffect, useState } from 'react'
import { useTranslation } from 'react-i18next'
import type {
  AgentConfigStatus,
  AgentProviderStatus,
  LanguageCode,
  McpManualConfig,
  McpPermissionMode,
  McpRegistrationTargetStatus,
  McpSnippetFormat,
  PermissionPolicy,
  ThemePreference
} from '../../../shared/types'
import { ModelIdEditor } from './ModelIdEditor'
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
  mcpPermissionMode: McpPermissionMode
  permissionPolicy: PermissionPolicy
  onLanguageChange: (language: LanguageCode) => void
  onThemePreferenceChange: (theme: ThemePreference) => void
  onTerminalFontFamilyChange: (family: string) => void
  onTerminalFontSizeChange: (size: number) => void
  onMcpIdleTimeoutMinutesChange: (minutes: number) => void
  onMcpMaxSessionsChange: (max: number) => void
  onMcpPermissionModeChange: (mode: McpPermissionMode) => void
  onPermissionPolicyChange: (policy: PermissionPolicy) => void
  onClose: () => void
}

type ProviderDraft = {
  key: string
  id: string
  name: string
  baseUrl: string
  models: string[]
  apiKey: string
  hasKey: boolean
}

let draftSeq = 0
function nextDraftKey(): string {
  draftSeq += 1
  return `draft-${draftSeq}`
}

function draftsFromStatus(status: AgentConfigStatus): ProviderDraft[] {
  return status.providers.map((p) => ({
    key: p.id,
    id: p.id,
    name: p.name,
    baseUrl: p.baseUrl,
    models: [...p.models],
    apiKey: '',
    hasKey: p.hasKey
  }))
}

function applyStatusToDrafts(prev: ProviderDraft[], status: AgentConfigStatus): ProviderDraft[] {
  const byId = new Map(status.providers.map((p) => [p.id, p]))
  const next: ProviderDraft[] = []
  const seen = new Set<string>()
  for (const draft of prev) {
    if (!draft.id) {
      next.push(draft)
      continue
    }
    const p = byId.get(draft.id)
    if (!p) continue
    seen.add(p.id)
    next.push({
      ...draft,
      name: p.name,
      baseUrl: p.baseUrl,
      models: [...p.models],
      apiKey: '',
      hasKey: p.hasKey
    })
  }
  for (const p of status.providers) {
    if (seen.has(p.id)) continue
    next.push({
      key: p.id,
      id: p.id,
      name: p.name,
      baseUrl: p.baseUrl,
      models: [...p.models],
      apiKey: '',
      hasKey: p.hasKey
    })
  }
  return next
}

/**
 * Agent provider settings. API keys are write-only: the backend stores them
 * in the OS keyring and reports back only whether one exists, so the renderer
 * never holds the secret and the field starts empty on every open. The
 * section is skipped entirely when the running bridge has no agent.
 */
function AgentSettingsSection(): React.JSX.Element | null {
  const { t } = useTranslation()
  const agent = window.api.agent
  const [drafts, setDrafts] = useState<ProviderDraft[]>([])
  const [expanded, setExpanded] = useState<string[]>([])
  const [busy, setBusy] = useState(false)
  const [ready, setReady] = useState(false)
  const [message, setMessage] = useState<string | null>(null)

  const patchDraft = (key: string, patch: Partial<ProviderDraft>): void => {
    setDrafts((prev) => prev.map((d) => (d.key === key ? { ...d, ...patch } : d)))
  }

  const toggleExpanded = (key: string): void => {
    setExpanded((prev) => (prev.includes(key) ? prev.filter((k) => k !== key) : [...prev, key]))
  }

  useEffect(() => {
    if (!agent) return
    void (async () => {
      try {
        const status = await agent.status()
        setDrafts(draftsFromStatus(status))
      } catch (err) {
        setMessage(err instanceof Error ? err.message : String(err))
      } finally {
        setReady(true)
      }
    })()
  }, [agent])

  if (!agent) return null

  const save = async (draft: ProviderDraft): Promise<void> => {
    setBusy(true)
    setMessage(null)
    try {
      let status = await agent.upsertProvider({
        ...(draft.id ? { id: draft.id } : {}),
        name: draft.name,
        baseUrl: draft.baseUrl,
        models: draft.models
      })
      const saved =
        draft.id !== ''
          ? status.providers.find((p) => p.id === draft.id)
          : newestProvider(status.providers, drafts)
      if (draft.apiKey !== '' && saved) {
        status = await agent.setProviderKey(saved.id, draft.apiKey)
      }
      setDrafts((prev) => applyStatusToDrafts(prev.filter((d) => d.key !== draft.key || d.id !== ''), status))
      setExpanded((prev) => prev.filter((k) => k !== draft.key && k !== saved?.id))
      setMessage(t('settings.agentSaved'))
    } catch (err) {
      setMessage(err instanceof Error ? err.message : String(err))
    } finally {
      setBusy(false)
    }
  }

  const clearKey = async (draft: ProviderDraft): Promise<void> => {
    if (!draft.id) return
    setBusy(true)
    setMessage(null)
    try {
      const status = await agent.setProviderKey(draft.id, '')
      setDrafts((prev) => applyStatusToDrafts(prev, status))
      setMessage(t('settings.agentKeyCleared'))
    } catch (err) {
      setMessage(err instanceof Error ? err.message : String(err))
    } finally {
      setBusy(false)
    }
  }

  const remove = async (draft: ProviderDraft): Promise<void> => {
    setBusy(true)
    setMessage(null)
    try {
      if (!draft.id) {
        setDrafts((prev) => prev.filter((d) => d.key !== draft.key))
        setExpanded((prev) => prev.filter((k) => k !== draft.key))
      } else {
        const status = await agent.deleteProvider(draft.id)
        setDrafts((prev) => applyStatusToDrafts(prev, status))
        setExpanded((prev) => prev.filter((k) => k !== draft.key && k !== draft.id))
        setMessage(t('settings.agentProviderDeleted'))
      }
    } catch (err) {
      setMessage(err instanceof Error ? err.message : String(err))
    } finally {
      setBusy(false)
    }
  }

  const addProvider = (): void => {
    const key = nextDraftKey()
    setDrafts((prev) => [
      ...prev,
      {
        key,
        id: '',
        name: '',
        baseUrl: '',
        models: [],
        apiKey: '',
        hasKey: false
      }
    ])
    setExpanded((prev) => [...prev, key])
  }

  return (
    <fieldset className="settings-section">
      <legend>{t('settings.agent')}</legend>
      <p className="settings-hint">{t('settings.agentHint')}</p>

      {drafts.map((draft, index) => {
        const open = expanded.includes(draft.key)
        const title = draft.name.trim() || t('settings.agentNewProvider')
        return (
          <div
            key={draft.key}
            className={`agent-provider-card${open ? ' is-expanded' : ''}`}
          >
            <button
              type="button"
              className="agent-provider-card-toggle"
              aria-expanded={open}
              aria-label={title}
              onClick={() => toggleExpanded(draft.key)}
            >
              <span
                className={`agent-provider-card-title${draft.name.trim() ? '' : ' is-placeholder'}`}
              >
                {title}
              </span>
              <span className="agent-provider-card-chevron" aria-hidden="true" />
            </button>
            <div
              className="agent-provider-card-collapse"
              aria-hidden={!open}
              inert={!open}
            >
              <div className="agent-provider-card-collapse-inner">
              <div className="agent-provider-card-body">
                <div className="form-field">
                  <span>{t('settings.agentName')}</span>
                  <input
                    type="text"
                    value={draft.name}
                    spellCheck={false}
                    aria-label={`${t('settings.agentName')} ${index + 1}`}
                    disabled={!ready || busy}
                    onChange={(e) => patchDraft(draft.key, { name: e.target.value })}
                  />
                </div>
                <div className="form-field">
                  <span>{t('settings.agentBaseUrl')}</span>
                  <input
                    type="text"
                    value={draft.baseUrl}
                    spellCheck={false}
                    aria-label={`${t('settings.agentBaseUrl')} ${index + 1}`}
                    disabled={!ready || busy}
                    onChange={(e) => patchDraft(draft.key, { baseUrl: e.target.value })}
                  />
                </div>
                <div className="form-field">
                  <span>{t('settings.agentModels')}</span>
                  <ModelIdEditor
                    value={draft.models}
                    disabled={!ready || busy}
                    ariaLabel={`${t('settings.agentModels')} ${index + 1}`}
                    onChange={(models) => patchDraft(draft.key, { models })}
                  />
                </div>
                <div className="form-field">
                  <span>{t('settings.agentApiKey')}</span>
                  <input
                    type="password"
                    value={draft.apiKey}
                    autoComplete="off"
                    spellCheck={false}
                    aria-label={`${t('settings.agentApiKey')} ${index + 1}`}
                    placeholder={draft.hasKey ? t('settings.agentKeyStored') : t('settings.agentKeyMissing')}
                    disabled={!ready || busy}
                    onChange={(e) => patchDraft(draft.key, { apiKey: e.target.value })}
                  />
                </div>
                <div className="mcp-register-actions">
                  <button
                    type="button"
                    className="btn-primary btn-sm"
                    disabled={!ready || busy}
                    onClick={() => void save(draft)}
                  >
                    {t('settings.agentSave')}
                  </button>
                  <button
                    type="button"
                    className="btn-secondary btn-sm"
                    disabled={!ready || busy || !draft.id || !draft.hasKey}
                    onClick={() => void clearKey(draft)}
                  >
                    {t('settings.agentClearKey')}
                  </button>
                  <button
                    type="button"
                    className="btn-secondary btn-sm"
                    disabled={!ready || busy}
                    onClick={() => void remove(draft)}
                  >
                    {t('settings.agentDeleteProvider')}
                  </button>
                </div>
              </div>
              </div>
            </div>
          </div>
        )
      })}

      <div className="mcp-register-actions">
        <button
          type="button"
          className="btn-secondary btn-sm"
          disabled={!ready || busy}
          onClick={addProvider}
        >
          {t('settings.agentAddProvider')}
        </button>
      </div>
      {message && <p className="mcp-register-message">{message}</p>}
    </fieldset>
  )
}

function newestProvider(
  providers: AgentProviderStatus[],
  drafts: ProviderDraft[]
): AgentProviderStatus | undefined {
  const known = new Set(drafts.map((d) => d.id).filter(Boolean))
  return providers.find((p) => !known.has(p.id)) ?? providers[providers.length - 1]
}

type SettingsPage = 'index' | 'general' | 'agent' | 'mcp'

function GeneralSettingsSection({
  language,
  themePreference,
  terminalFontFamily,
  terminalFontSize,
  permissionPolicy,
  onLanguageChange,
  onThemePreferenceChange,
  onTerminalFontFamilyChange,
  onTerminalFontSizeChange,
  onPermissionPolicyChange
}: Pick<
  SettingsModalProps,
  | 'language'
  | 'themePreference'
  | 'terminalFontFamily'
  | 'terminalFontSize'
  | 'permissionPolicy'
  | 'onLanguageChange'
  | 'onThemePreferenceChange'
  | 'onTerminalFontFamilyChange'
  | 'onTerminalFontSizeChange'
  | 'onPermissionPolicyChange'
>): React.JSX.Element {
  const { t } = useTranslation()
  const [fonts, setFonts] = useState<string[]>([])

  useEffect(() => {
    void (async () => {
      try {
        const list = await window.api.fonts.list()
        setFonts(list)
      } catch {
        setFonts([])
      }
    })()
  }, [])

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

  return (
    <div className="settings-page">
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
  )
}

function McpSettingsSection({
  mcpIdleTimeoutMinutes,
  mcpMaxSessions,
  mcpPermissionMode,
  onMcpIdleTimeoutMinutesChange,
  onMcpMaxSessionsChange,
  onMcpPermissionModeChange
}: Pick<
  SettingsModalProps,
  | 'mcpIdleTimeoutMinutes'
  | 'mcpMaxSessions'
  | 'mcpPermissionMode'
  | 'onMcpIdleTimeoutMinutesChange'
  | 'onMcpMaxSessionsChange'
  | 'onMcpPermissionModeChange'
>): React.JSX.Element {
  const { t } = useTranslation()
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
    <div className="settings-page">
      <fieldset className="settings-section settings-section--mcp">
        <p className="settings-hint">{t('settings.mcpHint')}</p>
        <div className="form-field">
          <span>{t('settings.mcpPermissionMode')}</span>
          <Select
            value={mcpPermissionMode}
            onChange={(v) => onMcpPermissionModeChange(v as McpPermissionMode)}
            aria-label={t('settings.mcpPermissionMode')}
            options={[
              { value: 'external', label: t('settings.mcpPermissionExternal') },
              { value: 'local', label: t('settings.mcpPermissionLocal') }
            ]}
          />
        </div>
        <p className="settings-hint">
          {mcpPermissionMode === 'local'
            ? t('settings.mcpPermissionLocalHint')
            : t('settings.mcpPermissionExternalHint')}
        </p>
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
                  mcpManual ? formatMcpLaunchCommand(mcpManual.command, mcpManual.args) : ''
                }
              />
            </label>
            <button
              type="button"
              className="btn-secondary btn-sm"
              disabled={mcpBusy || !mcpManual}
              onClick={() => {
                if (!mcpManual) return
                void copyText(
                  formatMcpLaunchCommand(mcpManual.command, mcpManual.args),
                  'settings.mcpCopyCommandOk'
                )
              }}
            >
              {t('settings.mcpCopyCommand')}
            </button>
          </div>
          <div className="mcp-format-tabs" role="tablist" aria-label={t('settings.mcpSnippetFormat')}>
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
              onClick={() => {
                const text = mcpManual?.snippets[mcpFormat]
                if (text) void copyText(text, 'settings.mcpCopyOk')
              }}
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
  )
}

function SettingsModalBody({
  language,
  themePreference,
  terminalFontFamily,
  terminalFontSize,
  mcpIdleTimeoutMinutes,
  mcpMaxSessions,
  mcpPermissionMode,
  permissionPolicy,
  onLanguageChange,
  onThemePreferenceChange,
  onTerminalFontFamilyChange,
  onTerminalFontSizeChange,
  onMcpIdleTimeoutMinutesChange,
  onMcpMaxSessionsChange,
  onMcpPermissionModeChange,
  onPermissionPolicyChange
}: Omit<SettingsModalProps, 'onClose'>): React.JSX.Element {
  const { t } = useTranslation()
  const requestClose = useModalClose()
  const [page, setPage] = useState<SettingsPage>('index')

  const navigate = (next: SettingsPage): void => {
    if (next !== page) setPage(next)
  }

  const title =
    page === 'general'
      ? t('settings.navGeneral')
      : page === 'agent'
        ? t('settings.navAgent')
        : page === 'mcp'
          ? t('settings.navMcp')
          : t('settings.title')

  return (
    <div className="settings-shell">
      <div className="settings-modal-header">
        {page !== 'index' ? (
          <button
            type="button"
            className="settings-modal-back"
            aria-label={t('settings.back')}
            onClick={() => navigate('index')}
          >
            <svg className="settings-modal-back-icon" viewBox="0 0 8 12" aria-hidden="true">
              <path
                d="M6.25 1.5L1.75 6L6.25 10.5"
                fill="none"
                stroke="currentColor"
                strokeWidth="1.75"
                strokeLinecap="round"
                strokeLinejoin="round"
              />
            </svg>
          </button>
        ) : null}
        <h3 id="settings-modal-title" className="modal-title">
          {title}
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

      {page === 'index' ? (
        <nav className="settings-nav" aria-label={t('settings.title')}>
          <button
            type="button"
            className="settings-nav-row"
            aria-label={t('settings.navGeneral')}
            onClick={() => navigate('general')}
          >
            <span className="settings-nav-copy">
              <span className="settings-nav-title">{t('settings.navGeneral')}</span>
              <span className="settings-nav-hint">{t('settings.navGeneralHint')}</span>
            </span>
            <span className="settings-nav-chevron" aria-hidden="true" />
          </button>
          <button
            type="button"
            className="settings-nav-row"
            aria-label={t('settings.navAgent')}
            onClick={() => navigate('agent')}
          >
            <span className="settings-nav-copy">
              <span className="settings-nav-title">{t('settings.navAgent')}</span>
              <span className="settings-nav-hint">{t('settings.navAgentHint')}</span>
            </span>
            <span className="settings-nav-chevron" aria-hidden="true" />
          </button>
          <button
            type="button"
            className="settings-nav-row"
            aria-label={t('settings.navMcp')}
            onClick={() => navigate('mcp')}
          >
            <span className="settings-nav-copy">
              <span className="settings-nav-title">{t('settings.navMcp')}</span>
              <span className="settings-nav-hint">{t('settings.navMcpHint')}</span>
            </span>
            <span className="settings-nav-chevron" aria-hidden="true" />
          </button>
        </nav>
      ) : page === 'general' ? (
        <GeneralSettingsSection
          language={language}
          themePreference={themePreference}
          terminalFontFamily={terminalFontFamily}
          terminalFontSize={terminalFontSize}
          permissionPolicy={permissionPolicy}
          onLanguageChange={onLanguageChange}
          onThemePreferenceChange={onThemePreferenceChange}
          onTerminalFontFamilyChange={onTerminalFontFamilyChange}
          onTerminalFontSizeChange={onTerminalFontSizeChange}
          onPermissionPolicyChange={onPermissionPolicyChange}
        />
      ) : page === 'agent' ? (
        <div className="settings-page">
          <AgentSettingsSection />
        </div>
      ) : (
        <McpSettingsSection
          mcpIdleTimeoutMinutes={mcpIdleTimeoutMinutes}
          mcpMaxSessions={mcpMaxSessions}
          mcpPermissionMode={mcpPermissionMode}
          onMcpIdleTimeoutMinutesChange={onMcpIdleTimeoutMinutesChange}
          onMcpMaxSessionsChange={onMcpMaxSessionsChange}
          onMcpPermissionModeChange={onMcpPermissionModeChange}
        />
      )}
    </div>
  )
}

export function SettingsModal(props: SettingsModalProps): React.JSX.Element {
  const { onClose, ...bodyProps } = props

  return (
    <ModalShell
      onClose={onClose}
      dialogClassName="settings-modal"
      labelledBy="settings-modal-title"
      motion="simple"
    >
      <SettingsModalBody {...bodyProps} />
    </ModalShell>
  )
}

import styles from '../settings.module.css'
import { bindStyles } from '../../../ui/class-names'
import { useEffect, useState } from 'react'
import { useTranslation } from 'react-i18next'
import type {
  McpManualConfig,
  McpRegistrationTargetStatus,
  McpSnippetFormat
} from '../../../../../shared/types'
import { FormField } from '../../../ui/field'
import { FormFieldset } from '../../../ui/fieldset'
import { Input } from '../../../ui/input'
import { TabsRoot, TabsList, TabsPanel, TabsTab } from '../../../ui/tabs'
import { Select } from '../../../ui/select'
import type { McpSettingsProps } from './types'

const classes = bindStyles(styles)

const MCP_SNIPPET_FORMATS: McpSnippetFormat[] = ['standard', 'vscode', 'opencode', 'codex']

function isMcpSnippetFormat(value: unknown): value is McpSnippetFormat {
  return typeof value === 'string' && (MCP_SNIPPET_FORMATS as string[]).includes(value)
}

function formatMcpLaunchCommand(command: string, args: string[]): string {
  const quote = (s: string): string => {
    if (s === '') return '""'
    if (/[\s"]/.test(s)) return `"${s.replace(/"/g, '\\"')}"`
    return s
  }
  return [command, ...args].map(quote).join(' ')
}

export function McpSettingsSection({
  mcpIdleTimeoutMinutes,
  mcpMaxSessions,
  mcpPermissionMode,
  onMcpIdleTimeoutMinutesChange,
  onMcpMaxSessionsChange,
  onMcpPermissionModeChange
}: McpSettingsProps): React.JSX.Element {
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
    <div className={classes('settings-page')}>
      <FormFieldset className={classes('settings-section settings-section--mcp')}>
        <p className={classes('settings-hint')}>{t('settings.mcpHint')}</p>
        <FormField label={t('settings.mcpPermissionMode')}>
          <Select
            value={mcpPermissionMode}
            onChange={(v) => onMcpPermissionModeChange(v as McpSettingsProps['mcpPermissionMode'])}
            aria-label={t('settings.mcpPermissionMode')}
            options={[
              { value: 'external', label: t('settings.mcpPermissionExternal') },
              { value: 'local', label: t('settings.mcpPermissionLocal') }
            ]}
          />
        </FormField>
        <p className={classes('settings-hint')}>
          {mcpPermissionMode === 'local'
            ? t('settings.mcpPermissionLocalHint')
            : t('settings.mcpPermissionExternalHint')}
        </p>
        <div className={classes('settings-mcp-options')}>
          <FormField label={t('settings.mcpIdleTimeout')}>
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
          </FormField>
          <FormField label={t('settings.mcpMaxSessions')}>
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
          </FormField>
        </div>

        <div className={classes('mcp-register-block')}>
          <div className={classes('mcp-register-title')}>{t('settings.mcpRegisterTitle')}</div>
          <p className={classes('settings-hint')}>{t('settings.mcpRegisterHint')}</p>
          <div className={classes('mcp-register-actions')}>
            <button
              type="button"
              className={classes('btn-primary btn-sm')}
              disabled={mcpBusy}
              onClick={() => void registerMcp('all')}
            >
              {t('settings.mcpRegisterAll')}
            </button>
          </div>
          <ul className={classes('mcp-register-list')}>
            {mcpTargets.map((row) => {
              const statusLabel = row.registered
                ? t('settings.mcpStatusRegistered')
                : row.stale
                  ? t('settings.mcpStatusStale')
                  : t('settings.mcpStatusMissing')
              return (
                <li key={row.id} className={classes('mcp-register-row')}>
                  <div className={classes('mcp-register-meta')}>
                    <span className={classes('mcp-register-name')}>{row.label}</span>
                    {row.configPath ? (
                      <span className={classes('mcp-register-path')} title={row.configPath}>
                        {row.configPath}
                      </span>
                    ) : null}
                    <span
                      className={classes(
                        `mcp-register-status${row.registered ? ' is-ok' : row.stale ? ' is-stale' : ''}`
                      )}
                    >
                      {statusLabel}
                    </span>
                  </div>
                  <button
                    type="button"
                    className={classes('btn-secondary btn-sm')}
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

        <div className={classes('mcp-register-block')}>
          <div className={classes('mcp-register-title')}>{t('settings.mcpOtherTitle')}</div>
          <p className={classes('settings-hint')}>{t('settings.mcpOtherHint')}</p>
          <div className={classes('mcp-launch-row')}>
            <FormField
              label={t('settings.mcpLaunchCommand')}
              className={classes('mcp-launch-field')}
            >
              <Input
                type="text"
                readOnly
                className={classes('mcp-launch-input')}
                aria-label={t('settings.mcpLaunchCommand')}
                value={mcpManual ? formatMcpLaunchCommand(mcpManual.command, mcpManual.args) : ''}
              />
            </FormField>
            <button
              type="button"
              className={classes('btn-secondary btn-sm')}
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
          <TabsRoot
            value={mcpFormat}
            onValueChange={(value) => {
              if (isMcpSnippetFormat(value)) setMcpFormat(value)
            }}
          >
            <TabsList
              className={classes('mcp-format-tabs')}
              aria-label={t('settings.mcpSnippetFormat')}
            >
              {MCP_SNIPPET_FORMATS.map((format) => (
                <TabsTab
                  key={format}
                  value={format}
                  className={classes(`mcp-format-tab${mcpFormat === format ? ' is-active' : ''}`)}
                >
                  {t(`settings.mcpFormat.${format}`)}
                </TabsTab>
              ))}
            </TabsList>
            {MCP_SNIPPET_FORMATS.map((format) => (
              <TabsPanel key={format} value={format}>
                <textarea
                  className={classes('mcp-snippet-preview')}
                  readOnly
                  spellCheck={false}
                  aria-label={t('settings.mcpSnippetPreview')}
                  value={mcpManual?.snippets[format] ?? ''}
                />
              </TabsPanel>
            ))}
          </TabsRoot>
          <div className={classes('mcp-register-actions')}>
            <button
              type="button"
              className={classes('btn-secondary btn-sm')}
              disabled={mcpBusy || !mcpManual?.snippets[mcpFormat]}
              onClick={() => {
                const text = mcpManual?.snippets[mcpFormat]
                if (text) void copyText(text, 'settings.mcpCopyOk')
              }}
            >
              {t('settings.mcpCopyConfig')}
            </button>
          </div>
          <div className={classes('mcp-paste-locations')}>
            <div className={classes('mcp-paste-title')}>{t('settings.mcpPasteLocations')}</div>
            <ul>
              <li>{t('settings.mcpPasteClaudeDesktop')}</li>
              <li>{t('settings.mcpPasteVscode')}</li>
              <li>{t('settings.mcpPasteWindsurf')}</li>
            </ul>
          </div>
          {mcpMessage && <p className={classes('mcp-register-message')}>{mcpMessage}</p>}
        </div>
      </FormFieldset>
    </div>
  )
}

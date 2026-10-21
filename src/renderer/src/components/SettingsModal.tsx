import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import type {
  LanguageCode,
  McpPermissionMode,
  PermissionPolicy,
  ThemePreference
} from '../../../shared/types'
import { ModalShell, useModalClose } from './ModalShell'
import { AgentRuntimeSettingsSection } from './settings/AgentRuntimeSettingsSection'
import { AgentSettingsSection } from './settings/AgentSettingsSection'
import { GeneralSettingsSection } from './settings/GeneralSettingsSection'
import { McpSettingsSection } from './settings/McpSettingsSection'

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

type SettingsPage = 'index' | 'general' | 'agent' | 'mcp'

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
          <AgentRuntimeSettingsSection />
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

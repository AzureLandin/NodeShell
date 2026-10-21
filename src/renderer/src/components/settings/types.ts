import type {
  LanguageCode,
  McpPermissionMode,
  PermissionPolicy,
  ThemePreference
} from '../../../../shared/types'

/** Props of the "General" settings page. */
export interface GeneralSettingsProps {
  language: LanguageCode
  themePreference: ThemePreference
  terminalFontFamily: string
  terminalFontSize: number
  permissionPolicy: PermissionPolicy
  onLanguageChange: (language: LanguageCode) => void
  onThemePreferenceChange: (theme: ThemePreference) => void
  onTerminalFontFamilyChange: (family: string) => void
  onTerminalFontSizeChange: (size: number) => void
  onPermissionPolicyChange: (policy: PermissionPolicy) => void
}

/** Props of the "MCP" settings page. */
export interface McpSettingsProps {
  mcpIdleTimeoutMinutes: number
  mcpMaxSessions: number
  mcpPermissionMode: McpPermissionMode
  onMcpIdleTimeoutMinutesChange: (minutes: number) => void
  onMcpMaxSessionsChange: (max: number) => void
  onMcpPermissionModeChange: (mode: McpPermissionMode) => void
}

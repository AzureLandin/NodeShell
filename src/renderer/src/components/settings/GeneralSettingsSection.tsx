import { useEffect, useState } from 'react'
import { useTranslation } from 'react-i18next'
import type { LanguageCode, PermissionPolicy, ThemePreference } from '../../../../shared/types'
import { FormField } from '../../ui/field'
import { FormFieldset } from '../../ui/fieldset'
import { Select } from '../Select'
import type { GeneralSettingsProps } from './types'

/**
 * Appearance, terminal and permission settings.
 *
 * Every control is a Base UI `Field` (label + control) and every group is a
 * Base UI `Fieldset`, which keeps the previous markup and styling intact.
 */
export function GeneralSettingsSection({
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
}: GeneralSettingsProps): React.JSX.Element {
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
      <FormField label={t('common.language')}>
        <Select
          value={language}
          onChange={(v) => onLanguageChange(v as LanguageCode)}
          aria-label={t('common.language')}
          options={[
            { value: 'zh', label: '中文' },
            { value: 'en', label: 'English' }
          ]}
        />
      </FormField>

      <FormFieldset legend={t('settings.appearance')} className="settings-section">
        <FormField label={t('settings.theme')}>
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
        </FormField>
      </FormFieldset>

      <FormFieldset legend={t('settings.terminal')} className="settings-section">
        <FormField label={t('settings.fontFamily')}>
          <Select
            value={terminalFontFamily}
            onChange={onTerminalFontFamilyChange}
            aria-label={t('settings.fontFamily')}
            options={fontSelectOptions}
          />
        </FormField>
        <FormField label={t('settings.fontSize')}>
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
        </FormField>
      </FormFieldset>

      <FormFieldset legend={t('settings.permissions')} className="settings-section">
        <p className="settings-hint">{t('settings.permissionsHint')}</p>
        <FormField label={t('settings.permissionPolicy')}>
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
        </FormField>
      </FormFieldset>
    </div>
  )
}

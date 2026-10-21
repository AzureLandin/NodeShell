import { useEffect, useState } from 'react'
import { useTranslation } from 'react-i18next'
import type {
  AgentContextCompression,
  AgentThinkingStrength,
  AppSettings
} from '../../../../shared/types'
import { FormField } from '../../ui/field'
import { FormFieldset } from '../../ui/fieldset'
import { FormNumberField } from '../../ui/number-field'
import { Select } from '../Select'

/** Lower bound of the agent context budget in tokens (0 = unlimited). */
const AGENT_CONTEXT_WINDOW_MIN = 4096
/** Upper bound of the agent context budget in tokens. */
const AGENT_CONTEXT_WINDOW_MAX = 1048576

/**
 * Clamp the estimated context budget the same way the settings store does:
 * 0 stays "unlimited", everything else is clamped into
 * [AGENT_CONTEXT_WINDOW_MIN, AGENT_CONTEXT_WINDOW_MAX].
 */
function clampAgentContextWindow(value: number): number {
  if (!Number.isFinite(value)) return 0
  const rounded = Math.round(value)
  if (rounded === 0) return 0
  return Math.min(AGENT_CONTEXT_WINDOW_MAX, Math.max(AGENT_CONTEXT_WINDOW_MIN, rounded))
}

function normalizeThinkingStrength(value: string | undefined): AgentThinkingStrength {
  if (value === 'low' || value === 'medium' || value === 'high') return value
  return 'auto'
}

function normalizeContextCompression(value: string | undefined): AgentContextCompression {
  return value === 'auto' ? 'auto' : 'off'
}

/**
 * Agent runtime settings. These are global (not per provider), so they live
 * in settings.json and are read and written through the existing settings
 * API — the provider CRUD stays untouched.
 */
export function AgentRuntimeSettingsSection(): React.JSX.Element {
  const { t } = useTranslation()
  const [contextWindow, setContextWindow] = useState<number | null>(0)
  const [savedContextWindow, setSavedContextWindow] = useState(0)
  const [thinkingStrength, setThinkingStrength] = useState<AgentThinkingStrength>('auto')
  const [compression, setCompression] = useState<AgentContextCompression>('off')
  const [ready, setReady] = useState(false)
  const [busy, setBusy] = useState(false)
  const [message, setMessage] = useState<string | null>(null)

  useEffect(() => {
    void (async () => {
      try {
        const settings = await window.api.settings.get()
        const tokens = settings?.agentContextWindow ?? 0
        setSavedContextWindow(tokens)
        setContextWindow(tokens)
        setThinkingStrength(normalizeThinkingStrength(settings?.agentThinkingStrength))
        setCompression(normalizeContextCompression(settings?.agentContextCompression))
      } catch (err) {
        setMessage(err instanceof Error ? err.message : String(err))
      } finally {
        setReady(true)
      }
    })()
  }, [])

  /** Persist one field; returns false when the backend rejected the write. */
  const persist = async (patch: Partial<AppSettings>): Promise<boolean> => {
    setBusy(true)
    setMessage(null)
    try {
      await window.api.settings.set(patch)
      setMessage(t('settings.agentRuntimeSaved'))
      return true
    } catch (err) {
      setMessage(err instanceof Error ? err.message : String(err))
      return false
    } finally {
      setBusy(false)
    }
  }

  const commitContextWindow = async (): Promise<void> => {
    if (contextWindow === null || !Number.isFinite(contextWindow)) {
      setContextWindow(savedContextWindow)
      return
    }
    const next = clampAgentContextWindow(contextWindow)
    if (next === savedContextWindow) {
      setContextWindow(next)
      return
    }
    const saved = await persist({ agentContextWindow: next })
    setSavedContextWindow(saved ? next : savedContextWindow)
    setContextWindow(saved ? next : savedContextWindow)
  }

  return (
    <FormFieldset legend={t('settings.agentRuntime')} className="settings-section">
      <p className="settings-hint">{t('settings.agentRuntimeHint')}</p>

      <FormField label={t('settings.agentContextWindow')}>
        <FormNumberField
          value={contextWindow}
          onValueChange={(next) => setContextWindow(next)}
          min={0}
          max={AGENT_CONTEXT_WINDOW_MAX}
          allowOutOfRange
          format={{ useGrouping: false }}
          disabled={!ready || busy}
          inputProps={{
            'aria-label': t('settings.agentContextWindow'),
            onBlur: () => void commitContextWindow(),
            onKeyDown: (e) => {
              if (e.key === 'Enter') void commitContextWindow()
            }
          }}
        />
      </FormField>
      <p className="settings-hint">{t('settings.agentContextWindowHint')}</p>

      <FormField label={t('settings.agentThinkingStrength')}>
        <Select
          value={thinkingStrength}
          aria-label={t('settings.agentThinkingStrength')}
          disabled={!ready || busy}
          onChange={(v) => {
            const next = normalizeThinkingStrength(v)
            setThinkingStrength(next)
            void persist({ agentThinkingStrength: next })
          }}
          options={[
            { value: 'auto', label: t('settings.agentThinkingAuto') },
            { value: 'low', label: t('settings.agentThinkingLow') },
            { value: 'medium', label: t('settings.agentThinkingMedium') },
            { value: 'high', label: t('settings.agentThinkingHigh') }
          ]}
        />
      </FormField>

      <FormField label={t('settings.agentContextCompression')}>
        <Select
          value={compression}
          aria-label={t('settings.agentContextCompression')}
          disabled={!ready || busy}
          onChange={(v) => {
            const next = normalizeContextCompression(v)
            setCompression(next)
            void persist({ agentContextCompression: next })
          }}
          options={[
            { value: 'off', label: t('settings.agentCompressionOff') },
            { value: 'auto', label: t('settings.agentCompressionAuto') }
          ]}
        />
      </FormField>

      {message && <p className="mcp-register-message">{message}</p>}
    </FormFieldset>
  )
}

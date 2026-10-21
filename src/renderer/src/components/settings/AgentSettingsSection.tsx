import { useEffect, useState } from 'react'
import { useTranslation } from 'react-i18next'
import type { AgentConfigStatus, AgentProviderStatus } from '../../../../shared/types'
import { FormField } from '../../ui/field'
import { FormFieldset } from '../../ui/fieldset'
import { Input } from '../../ui/input'
import { ModelIdEditor } from '../ModelIdEditor'

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

function newestProvider(
  providers: AgentProviderStatus[],
  drafts: ProviderDraft[]
): AgentProviderStatus | undefined {
  const known = new Set(drafts.map((d) => d.id).filter(Boolean))
  return providers.find((p) => !known.has(p.id)) ?? providers[providers.length - 1]
}

/**
 * Agent provider settings. API keys are write-only: the backend stores them
 * in the OS keyring and reports back only whether one exists, so the renderer
 * never holds the secret and the field starts empty on every open. The
 * section is skipped entirely when the running bridge has no agent.
 */
export function AgentSettingsSection(): React.JSX.Element | null {
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
      setDrafts((prev) =>
        applyStatusToDrafts(
          prev.filter((d) => d.key !== draft.key || d.id !== ''),
          status
        )
      )
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
    <FormFieldset legend={t('settings.agent')} className="settings-section">
      <p className="settings-hint">{t('settings.agentHint')}</p>

      {drafts.map((draft, index) => {
        const open = expanded.includes(draft.key)
        const title = draft.name.trim() || t('settings.agentNewProvider')
        return (
          <div key={draft.key} className={`agent-provider-card${open ? ' is-expanded' : ''}`}>
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
            <div className="agent-provider-card-collapse" aria-hidden={!open} inert={!open}>
              <div className="agent-provider-card-collapse-inner">
                <div className="agent-provider-card-body">
                  <FormField label={t('settings.agentName')}>
                    <Input
                      type="text"
                      value={draft.name}
                      spellCheck={false}
                      aria-label={`${t('settings.agentName')} ${index + 1}`}
                      disabled={!ready || busy}
                      onChange={(e) => patchDraft(draft.key, { name: e.target.value })}
                    />
                  </FormField>
                  <FormField label={t('settings.agentBaseUrl')}>
                    <Input
                      type="text"
                      value={draft.baseUrl}
                      spellCheck={false}
                      aria-label={`${t('settings.agentBaseUrl')} ${index + 1}`}
                      disabled={!ready || busy}
                      onChange={(e) => patchDraft(draft.key, { baseUrl: e.target.value })}
                    />
                  </FormField>
                  <FormField label={t('settings.agentModels')}>
                    <ModelIdEditor
                      value={draft.models}
                      disabled={!ready || busy}
                      ariaLabel={`${t('settings.agentModels')} ${index + 1}`}
                      onChange={(models) => patchDraft(draft.key, { models })}
                    />
                  </FormField>
                  <FormField label={t('settings.agentApiKey')}>
                    <Input
                      type="password"
                      value={draft.apiKey}
                      autoComplete="off"
                      spellCheck={false}
                      aria-label={`${t('settings.agentApiKey')} ${index + 1}`}
                      placeholder={
                        draft.hasKey ? t('settings.agentKeyStored') : t('settings.agentKeyMissing')
                      }
                      disabled={!ready || busy}
                      onChange={(e) => patchDraft(draft.key, { apiKey: e.target.value })}
                    />
                  </FormField>
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
    </FormFieldset>
  )
}

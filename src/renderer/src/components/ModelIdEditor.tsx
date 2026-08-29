import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { FontAwesomeIcon } from '@fortawesome/react-fontawesome'
import { faPlus, faXmark } from '@fortawesome/free-solid-svg-icons'

export const DEFAULT_MAX_ITEMS = 32
export const DEFAULT_MAX_ITEM_LENGTH = 512

export interface ModelIdEditorProps {
  value: string[]
  onChange: (models: string[]) => void
  disabled?: boolean
  maxItems?: number
  maxItemLength?: number
  ariaLabel?: string
}

export function ModelIdEditor({
  value,
  onChange,
  disabled = false,
  maxItems = DEFAULT_MAX_ITEMS,
  maxItemLength = DEFAULT_MAX_ITEM_LENGTH,
  ariaLabel
}: ModelIdEditorProps): React.JSX.Element {
  const { t } = useTranslation()
  const [input, setInput] = useState('')
  const [error, setError] = useState<string | null>(null)

  const addTokens = (raw: string): void => {
    if (disabled) return
    const trimmedRaw = raw.trim()
    if (!trimmedRaw) return

    const tokens = trimmedRaw
      .split(/[,\r\n]+/)
      .map((tok) => tok.trim())
      .filter(Boolean)

    if (tokens.length === 0) return

    const next = [...value]
    let hadError = false

    for (const token of tokens) {
      if (token.length > maxItemLength) {
        setError(t('settings.agentModelsTooLong'))
        hadError = true
        break
      }
      if (next.includes(token)) {
        if (tokens.length === 1) {
          setError(t('settings.agentModelsDuplicate'))
          hadError = true
        }
        continue
      }
      if (next.length >= maxItems) {
        setError(t('settings.agentModelsLimit'))
        hadError = true
        break
      }
      next.push(token)
    }

    if (next.length !== value.length) {
      onChange(next)
      setInput('')
      if (!hadError) {
        setError(null)
      }
    } else if (!hadError && tokens.length > 0) {
      // All were duplicates
      setError(t('settings.agentModelsDuplicate'))
    }
  }

  const handleKeyDown = (e: React.KeyboardEvent<HTMLInputElement>): void => {
    if (e.nativeEvent.isComposing) return

    if (e.key === 'Enter' || e.key === ',') {
      e.preventDefault()
      addTokens(input)
    } else if (e.key === 'Backspace' && input === '' && value.length > 0) {
      e.preventDefault()
      onChange(value.slice(0, -1))
      setError(null)
    } else if (e.key === 'Escape') {
      e.preventDefault()
      setInput('')
      setError(null)
    }
  }

  const handleBlur = (): void => {
    if (input.trim()) {
      addTokens(input)
    }
  }

  const handlePaste = (e: React.ClipboardEvent<HTMLInputElement>): void => {
    const text = e.clipboardData.getData('text')
    if (/[,\r\n]/.test(text)) {
      e.preventDefault()
      addTokens(text)
    }
  }

  const removeModel = (index: number): void => {
    if (disabled) return
    onChange(value.filter((_, i) => i !== index))
    setError(null)
  }

  const isLimitReached = value.length >= maxItems

  return (
    <div
      className={`model-id-editor${disabled ? ' is-disabled' : ''}${error ? ' has-error' : ''}`}
    >
      <div className="model-id-editor-box">
        {value.length > 0 && (
          <div className="model-id-editor-list" role="list">
            {value.map((model, index) => (
              <span key={`${model}-${index}`} className="model-id-chip" role="listitem" title={model}>
                <span className="model-id-chip-label">{model}</span>
                <button
                  type="button"
                  className="model-id-chip-remove"
                  disabled={disabled}
                  aria-label={t('settings.agentModelRemove', { model })}
                  onClick={() => removeModel(index)}
                >
                  <FontAwesomeIcon icon={faXmark} aria-hidden />
                </button>
              </span>
            ))}
          </div>
        )}

        <div className="model-id-editor-input-row">
          <input
            type="text"
            className="model-id-editor-input"
            value={input}
            disabled={disabled || isLimitReached}
            placeholder={
              isLimitReached
                ? t('settings.agentModelsLimit')
                : t('settings.agentModelPlaceholder')
            }
            aria-label={ariaLabel ?? t('settings.agentModels')}
            onChange={(e) => {
              setInput(e.target.value)
              if (error) setError(null)
            }}
            onKeyDown={handleKeyDown}
            onBlur={handleBlur}
            onPaste={handlePaste}
            spellCheck={false}
          />
          <button
            type="button"
            className="model-id-editor-add-btn"
            disabled={disabled || !input.trim() || isLimitReached}
            aria-label={t('settings.agentModelAdd')}
            title={t('settings.agentModelAdd')}
            onClick={() => addTokens(input)}
          >
            <FontAwesomeIcon icon={faPlus} aria-hidden />
          </button>
        </div>
      </div>

      <div className="model-id-editor-meta">
        <span className={`model-id-editor-hint${error ? ' is-error' : ''}`}>
          {error ?? t('settings.agentModelsMeta')}
        </span>
        <span className="model-id-editor-count">
          {value.length} / {maxItems}
        </span>
      </div>
    </div>
  )
}

import styles from './agent.module.css'
import { bindStyles } from '../../ui/class-names'
import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { FontAwesomeIcon } from '@fortawesome/react-fontawesome'
import { faCheck, faChevronDown, faChevronRight, faXmark } from '@fortawesome/free-solid-svg-icons'

const classes = bindStyles(styles)

interface AgentToolCallProps {
  name: string
  summary: string
  ok: boolean
  detail?: string
}

function verbKey(name: string): string {
  switch (name) {
    case 'bash':
      return 'agent.toolRan'
    case 'sftp_list':
      return 'agent.toolListed'
    case 'sftp_read':
      return 'agent.toolRead'
    case 'sftp_write':
      return 'agent.toolWrote'
    default:
      return 'agent.toolCalled'
  }
}

/**
 * One finished tool call, Codex-style: a collapsed verb + summary row that
 * expands into a nested command block. Failures start open so the error is
 * visible without an extra click.
 */
export function AgentToolCall({
  name,
  summary,
  ok,
  detail
}: AgentToolCallProps): React.JSX.Element {
  const { t } = useTranslation()
  const [open, setOpen] = useState(!ok)
  const verb = t(verbKey(name), { name })
  const command = name === 'bash' ? `$ ${summary}` : summary
  const label = `${verb} ${summary}${ok ? '' : ` (${t('agent.toolFailed')})`}`

  return (
    <div className={classes(`agent-tool${ok ? '' : ' is-failed'}${open ? ' is-open' : ''}`)}>
      <button
        type="button"
        className={classes('agent-tool-head')}
        aria-expanded={open}
        aria-label={label}
        onClick={() => setOpen((v) => !v)}
      >
        <FontAwesomeIcon
          icon={open ? faChevronDown : faChevronRight}
          className={classes('agent-tool-chevron')}
        />
        <FontAwesomeIcon icon={ok ? faCheck : faXmark} className={classes('agent-tool-status')} />
        <span className={classes('agent-tool-verb')}>{verb}</span>
        {!open && <span className={classes('agent-tool-summary')}>{summary}</span>}
      </button>
      {open && (
        <div className={classes('agent-tool-body')}>
          <pre className={classes('agent-tool-cmd')}>{command}</pre>
          {detail ? <pre className={classes('agent-tool-detail')}>{detail}</pre> : null}
        </div>
      )}
    </div>
  )
}

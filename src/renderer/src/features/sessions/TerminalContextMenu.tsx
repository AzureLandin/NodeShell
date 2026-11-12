import styles from './sessions.module.css'
import { bindStyles } from '../../ui/class-names'
import type { ReactNode } from 'react'
import { useTranslation } from 'react-i18next'
import { ContextMenuSurface, type ContextMenuSurfaceProps } from '../../ui/context-menu'
import { MenuItem, MenuSeparator } from '../../ui/menu'

const classes = bindStyles(styles)

export interface TerminalContextMenuProps {
  open: boolean
  x: number
  y: number
  canCopy: boolean
  onCopy: () => void
  onPaste: () => void
  onClear: () => void
  onClose: () => void
  finalFocus?: ContextMenuSurfaceProps['finalFocus']
}

/**
 * Right-click menu of the terminal.
 *
 * The host owns the open state; Base UI owns the positioning at
 * the pointer, the roving focus, the Escape/outside-press dismissal and the
 * close-on-select behaviour.
 */
export function TerminalContextMenu({
  open,
  x,
  y,
  canCopy,
  onCopy,
  onPaste,
  onClear,
  onClose,
  finalFocus
}: TerminalContextMenuProps): ReactNode {
  const { t } = useTranslation()

  return (
    <ContextMenuSurface
      x={x}
      y={y}
      open={open}
      onOpenChange={(next) => {
        if (!next) onClose()
      }}
      finalFocus={finalFocus}
      className={classes('terminal-context-menu')}
      testId="terminal-context-menu"
    >
      <MenuItem
        className={classes('terminal-context-menu-item')}
        disabled={!canCopy}
        onClick={onCopy}
      >
        {t('terminal.copy')}
      </MenuItem>
      <MenuItem className={classes('terminal-context-menu-item')} onClick={onPaste}>
        {t('terminal.paste')}
      </MenuItem>
      <MenuSeparator className={classes('terminal-context-menu-separator')} />
      <MenuItem className={classes('terminal-context-menu-item')} onClick={onClear}>
        {t('terminal.clearScreen')}
      </MenuItem>
    </ContextMenuSurface>
  )
}

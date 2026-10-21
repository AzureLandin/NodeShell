import { useCallback, useState, type ReactNode } from 'react'
import { useTranslation } from 'react-i18next'
import { ContextMenuSurface } from '../ui/context-menu'
import { MenuItem, MenuSeparator } from '../ui/menu'

export interface TerminalContextMenuProps {
  x: number
  y: number
  canCopy: boolean
  onCopy: () => void
  onPaste: () => void
  onClear: () => void
  onClose: () => void
}

/**
 * Right-click menu of the terminal.
 *
 * The menu is mounted only while it is open; Base UI owns the positioning at
 * the pointer, the roving focus, the Escape/outside-press dismissal and the
 * close-on-select behaviour.
 */
export function TerminalContextMenu({
  x,
  y,
  canCopy,
  onCopy,
  onPaste,
  onClear,
  onClose
}: TerminalContextMenuProps): ReactNode {
  const { t } = useTranslation()
  const [open, setOpen] = useState(true)

  const handleOpenChange = useCallback(
    (next: boolean): void => {
      setOpen(next)
      if (!next) {
        onClose()
      }
    },
    [onClose]
  )

  return (
    <ContextMenuSurface
      x={x}
      y={y}
      open={open}
      onOpenChange={handleOpenChange}
      className="terminal-context-menu"
      testId="terminal-context-menu"
    >
      <MenuItem className="terminal-context-menu-item" disabled={!canCopy} onClick={onCopy}>
        {t('terminal.copy')}
      </MenuItem>
      <MenuItem className="terminal-context-menu-item" onClick={onPaste}>
        {t('terminal.paste')}
      </MenuItem>
      <MenuSeparator className="terminal-context-menu-separator" />
      <MenuItem className="terminal-context-menu-item" onClick={onClear}>
        {t('terminal.clearScreen')}
      </MenuItem>
    </ContextMenuSurface>
  )
}

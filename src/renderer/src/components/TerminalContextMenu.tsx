import type { ReactNode } from 'react'
import { useTranslation } from 'react-i18next'

export interface TerminalContextMenuProps {
  x: number
  y: number
  canCopy: boolean
  onCopy: () => void
  onPaste: () => void
  onClear: () => void
  onClose: () => void
}

const MENU_WIDTH = 160
const MENU_HEIGHT = 96

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
  const left = Math.min(x, Math.max(0, window.innerWidth - MENU_WIDTH))
  const top = Math.min(y, Math.max(0, window.innerHeight - MENU_HEIGHT))
  return (
    <div
      role="menu"
      className="terminal-context-menu"
      style={{ left, top }}
      onContextMenu={(e) => e.preventDefault()}
      data-testid="terminal-context-menu"
    >
      <button
        type="button"
        role="menuitem"
        className="terminal-context-menu-item"
        disabled={!canCopy}
        onClick={() => {
          onCopy()
          onClose()
        }}
      >
        {t('terminal.copy')}
      </button>
      <button
        type="button"
        role="menuitem"
        className="terminal-context-menu-item"
        onClick={() => {
          onPaste()
          onClose()
        }}
      >
        {t('terminal.paste')}
      </button>
      <div role="separator" className="terminal-context-menu-separator" />
      <button
        type="button"
        role="menuitem"
        className="terminal-context-menu-item"
        onClick={() => {
          onClear()
          onClose()
        }}
      >
        {t('terminal.clearScreen')}
      </button>
    </div>
  )
}

import type { ReactNode } from 'react'
import { createPortal } from 'react-dom'
import { useTranslation } from 'react-i18next'

export interface SftpContextMenuProps {
  x: number
  y: number
  canEdit: boolean
  canDownload: boolean
  onEdit: () => void
  onDownload: () => void
  onRename: () => void
  onDelete: () => void
  onClose: () => void
}

const MENU_WIDTH = 160
const ITEM_HEIGHT = 28
const SEPARATOR_HEIGHT = 9
const MENU_PAD = 8

function menuHeight(canEdit: boolean, canDownload: boolean): number {
  const items = (canEdit ? 1 : 0) + (canDownload ? 1 : 0) + 2
  return MENU_PAD + items * ITEM_HEIGHT + SEPARATOR_HEIGHT
}

export function SftpContextMenu({
  x,
  y,
  canEdit,
  canDownload,
  onEdit,
  onDownload,
  onRename,
  onDelete,
  onClose
}: SftpContextMenuProps): ReactNode {
  const { t } = useTranslation()
  const height = menuHeight(canEdit, canDownload)
  const left = Math.min(x, Math.max(0, window.innerWidth - MENU_WIDTH))
  const top = Math.min(y, Math.max(0, window.innerHeight - height))

  const run = (action: () => void): void => {
    onClose()
    action()
  }

  return createPortal(
    <div
      role="menu"
      className="sftp-context-menu"
      style={{ left, top }}
      onContextMenu={(e) => e.preventDefault()}
      data-testid="sftp-context-menu"
    >
      {canEdit ? (
        <button
          type="button"
          role="menuitem"
          className="sftp-context-menu-item"
          onClick={() => run(onEdit)}
        >
          {t('sftp.edit')}
        </button>
      ) : null}
      {canDownload ? (
        <button
          type="button"
          role="menuitem"
          className="sftp-context-menu-item"
          onClick={() => run(onDownload)}
        >
          {t('sftp.download')}
        </button>
      ) : null}
      <button
        type="button"
        role="menuitem"
        className="sftp-context-menu-item"
        onClick={() => run(onRename)}
      >
        {t('sftp.rename')}
      </button>
      <div role="separator" className="sftp-context-menu-separator" />
      <button
        type="button"
        role="menuitem"
        className="sftp-context-menu-item is-danger"
        onClick={() => run(onDelete)}
      >
        {t('sftp.delete')}
      </button>
    </div>,
    document.body
  )
}

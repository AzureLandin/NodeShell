import { useCallback, useState, type ReactNode } from 'react'
import { useTranslation } from 'react-i18next'
import { ContextMenuSurface } from '../ui/context-menu'
import { MenuItem, MenuSeparator } from '../ui/menu'

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

/**
 * Right-click menu of a file row.
 *
 * The menu is mounted only while it is open; Base UI owns the positioning at
 * the pointer, the roving focus, the Escape/outside-press dismissal and the
 * close-on-select behaviour.
 */
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
      className="sftp-context-menu"
      testId="sftp-context-menu"
    >
      {canEdit ? (
        <MenuItem className="sftp-context-menu-item" onClick={onEdit}>
          {t('sftp.edit')}
        </MenuItem>
      ) : null}
      {canDownload ? (
        <MenuItem className="sftp-context-menu-item" onClick={onDownload}>
          {t('sftp.download')}
        </MenuItem>
      ) : null}
      <MenuItem className="sftp-context-menu-item" onClick={onRename}>
        {t('sftp.rename')}
      </MenuItem>
      <MenuSeparator className="sftp-context-menu-separator" />
      <MenuItem className="sftp-context-menu-item is-danger" onClick={onDelete}>
        {t('sftp.delete')}
      </MenuItem>
    </ContextMenuSurface>
  )
}

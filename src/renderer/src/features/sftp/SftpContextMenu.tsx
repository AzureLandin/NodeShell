import styles from './sftp.module.css'
import { bindStyles } from '../../ui/class-names'
import type { ReactNode } from 'react'
import { useTranslation } from 'react-i18next'
import { ContextMenuSurface, type ContextMenuSurfaceProps } from '../../ui/context-menu'
import { MenuItem, MenuSeparator } from '../../ui/menu'

const classes = bindStyles(styles)

export interface SftpContextMenuProps {
  open: boolean
  x: number
  y: number
  canEdit: boolean
  canDownload: boolean
  onEdit: () => void
  onDownload: () => void
  onRename: () => void
  onDelete: () => void
  onClose: () => void
  finalFocus?: ContextMenuSurfaceProps['finalFocus']
}

/**
 * Right-click menu of a file row.
 *
 * The host owns the open state; Base UI owns the positioning at
 * the pointer, the roving focus, the Escape/outside-press dismissal and the
 * close-on-select behaviour.
 */
export function SftpContextMenu({
  open,
  x,
  y,
  canEdit,
  canDownload,
  onEdit,
  onDownload,
  onRename,
  onDelete,
  onClose,
  finalFocus
}: SftpContextMenuProps): ReactNode {
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
      className={classes('sftp-context-menu')}
      testId="sftp-context-menu"
    >
      {canEdit ? (
        <MenuItem className={classes('sftp-context-menu-item')} onClick={onEdit}>
          {t('sftp.edit')}
        </MenuItem>
      ) : null}
      {canDownload ? (
        <MenuItem className={classes('sftp-context-menu-item')} onClick={onDownload}>
          {t('sftp.download')}
        </MenuItem>
      ) : null}
      <MenuItem className={classes('sftp-context-menu-item')} onClick={onRename}>
        {t('sftp.rename')}
      </MenuItem>
      <MenuSeparator className={classes('sftp-context-menu-separator')} />
      <MenuItem className={classes('sftp-context-menu-item is-danger')} onClick={onDelete}>
        {t('sftp.delete')}
      </MenuItem>
    </ContextMenuSurface>
  )
}

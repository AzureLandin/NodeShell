import type { ComponentProps } from 'react'
import { HostPickerModal } from '../features/hosts'
import { SettingsModal } from '../features/settings'
import { PasswordModal } from '../features/sessions'
import { PermissionModal } from '../features/permissions'
import { ConfirmModal } from '../ui/ConfirmModal'
import type { useConfirmationQueue } from './use-confirmation-queue'

export function AppOverlays({
  hosts,
  settings,
  password,
  confirmation,
  permission
}: {
  hosts: ComponentProps<typeof HostPickerModal> | null
  settings: ComponentProps<typeof SettingsModal> | null
  password: ComponentProps<typeof PasswordModal> | null
  confirmation: ReturnType<typeof useConfirmationQueue>
  permission: (ComponentProps<typeof PermissionModal> & { key: string }) | null
}): React.JSX.Element {
  const current = confirmation.current
  return (
    <>
      {hosts && <HostPickerModal {...hosts} />}
      {settings && <SettingsModal {...settings} />}
      {password && <PasswordModal {...password} />}
      {current && (
        <ConfirmModal
          key={current.id}
          {...current}
          onConfirm={() => confirmation.settle(current.id, true)}
          onCancel={() => confirmation.settle(current.id, false)}
        />
      )}
      {permission && (
        <PermissionModal
          key={permission.key}
          request={permission.request}
          onDecide={permission.onDecide}
        />
      )}
    </>
  )
}

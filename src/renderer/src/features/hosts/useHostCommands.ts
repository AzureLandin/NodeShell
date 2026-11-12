import { useLayoutEffect, useRef } from 'react'
import { useTranslation } from 'react-i18next'
import type { ConnectOptions, HostConfig } from '../../../../shared/types'
import { useNotify } from '../../ui/use-notify'
import type { HostFormSubmit } from './host-form-model'
import type { useHosts } from './useHosts'

export function useHostCommands(
  hosts: ReturnType<typeof useHosts>,
  connect: (host: HostConfig, options?: ConnectOptions) => void,
  reportCredentialError: (error: unknown) => void
): {
  create: (result: HostFormSubmit) => Promise<void>
  update: (id: string, result: HostFormSubmit) => Promise<void>
} {
  const { t } = useTranslation()
  const notify = useNotify()
  const lifetime = useRef(0)
  useLayoutEffect(() => {
    lifetime.current += 1
    return () => {
      lifetime.current += 1
    }
  }, [])

  const create = async ({ input, password }: HostFormSubmit): Promise<void> => {
    const generation = lifetime.current
    const host = await hosts.create(input)
    if (generation === lifetime.current) connect(host, password ? { password } : {})
  }

  const update = async (id: string, { input, password }: HostFormSubmit): Promise<void> => {
    const generation = lifetime.current
    await hosts.update(id, input)
    if (!password || generation !== lifetime.current) return
    try {
      const available = await window.api.credentials.isAvailable()
      if (generation !== lifetime.current) return
      if (!available) {
        notify(t('auth.credentialsUnavailable'))
        return
      }
      await window.api.credentials.save(id, {
        password,
        ...(input.privateKeyPath ? { privateKeyPath: input.privateKeyPath } : {})
      })
      if (generation === lifetime.current) await hosts.refresh()
    } catch (error) {
      if (generation === lifetime.current) reportCredentialError(error)
    }
  }

  return { create, update }
}

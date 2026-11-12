import { useQuery, useQueryClient } from '@tanstack/react-query'
import { useCallback, useLayoutEffect, useRef } from 'react'
import { parseIpcThrownError } from '../../../../shared/ipc-error'
import type { HostConfig, HostInput } from '../../../../shared/types'
import { HOSTS_QUERY_KEY, hostsQueryOptions } from './hosts-query'

const EMPTY_HOSTS: HostConfig[] = []

export function useHosts(): {
  hosts: HostConfig[]
  loading: boolean
  refreshing: boolean
  error: string | null
  hasData: boolean
  refresh: () => Promise<HostConfig[] | undefined>
  create: (input: HostInput) => Promise<HostConfig>
  update: (id: string, patch: Partial<HostInput>) => Promise<void>
  remove: (id: string) => Promise<void>
} {
  const client = useQueryClient()
  const query = useQuery(hostsQueryOptions)
  const lifetime = useRef(0)
  const mounted = useRef(false)

  useLayoutEffect(() => {
    mounted.current = true
    lifetime.current += 1
    return () => {
      mounted.current = false
      lifetime.current += 1
    }
  }, [])

  const refresh = useCallback(async (): Promise<HostConfig[] | undefined> => {
    if (!mounted.current) return undefined
    const generation = lifetime.current
    await client.cancelQueries({ queryKey: HOSTS_QUERY_KEY, exact: true })
    if (generation !== lifetime.current) return undefined
    await client.invalidateQueries({ queryKey: HOSTS_QUERY_KEY, exact: true, refetchType: 'none' })
    if (generation !== lifetime.current) return undefined
    try {
      return await client.fetchQuery(hostsQueryOptions)
    } catch {
      // Query owns the local read error. A failed refresh never rejects a successful command.
      return undefined
    }
  }, [client])

  const reconcile = async (
    generation: number,
    apply: (hosts: HostConfig[]) => HostConfig[]
  ): Promise<void> => {
    if (generation !== lifetime.current) return
    await client.cancelQueries({ queryKey: HOSTS_QUERY_KEY, exact: true })
    if (generation !== lifetime.current) return
    // Apply only confirmed backend writes, before refetch, so a failed read does not undo them.
    client.setQueryData<HostConfig[]>(HOSTS_QUERY_KEY, (hosts) => apply(hosts ?? []))
    await refresh()
  }

  const create = async (input: HostInput): Promise<HostConfig> => {
    const generation = lifetime.current
    const host = await window.api.hosts.create(input)
    await reconcile(generation, (hosts) => [...hosts.filter((h) => h.id !== host.id), host])
    return host
  }

  const update = async (id: string, patch: Partial<HostInput>): Promise<void> => {
    const generation = lifetime.current
    const host = await window.api.hosts.update(id, patch)
    await reconcile(generation, (hosts) => hosts.map((h) => (h.id === id ? host : h)))
  }

  const remove = async (id: string): Promise<void> => {
    const generation = lifetime.current
    await window.api.hosts.remove(id)
    await reconcile(generation, (hosts) => hosts.filter((h) => h.id !== id))
  }

  return {
    hosts: query.data ?? EMPTY_HOSTS,
    loading: query.isPending && query.isFetching,
    refreshing: query.isFetching,
    error: query.error ? parseIpcThrownError(query.error).message : null,
    hasData: query.data !== undefined,
    refresh,
    create,
    update,
    remove
  }
}

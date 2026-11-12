import { queryOptions } from '@tanstack/react-query'

export const HOSTS_QUERY_KEY = ['hosts', 'list'] as const

export const hostsQueryOptions = queryOptions({
  queryKey: HOSTS_QUERY_KEY,
  staleTime: Infinity,
  queryFn: async ({ signal }) => {
    const hosts = await window.api.hosts.list()
    // Reading the signal also lets Query cancel/revert when the last observer leaves.
    // The Wails call itself keeps running; its late result must not populate the cache.
    signal.throwIfAborted()
    return hosts
  }
})

import { QueryClient } from '@tanstack/react-query'

/** IPC reads are local operations: browser connectivity must not pause them. */
export function createAppQueryClient(): QueryClient {
  return new QueryClient({
    defaultOptions: {
      queries: {
        retry: false,
        refetchOnWindowFocus: false,
        refetchOnReconnect: false,
        networkMode: 'always',
        gcTime: 5 * 60 * 1000
      },
      mutations: { retry: false, networkMode: 'always' }
    }
  })
}

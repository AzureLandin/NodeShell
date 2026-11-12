// @vitest-environment jsdom
import { StrictMode, type ReactNode } from 'react'
import {
  QueryClientProvider,
  focusManager,
  onlineManager,
  useQueryClient
} from '@tanstack/react-query'
import { afterEach, describe, expect, it } from 'vitest'
import { act, render, renderHook, waitFor } from '@testing-library/react'
import { createAppQueryClient } from '../../src/renderer/src/app/query-client'
import { AppQueryProvider } from '../../src/renderer/src/app/query-provider'
import { HOSTS_QUERY_KEY } from '../../src/renderer/src/features/hosts/hosts-query'
import { useHosts } from '../../src/renderer/src/features/hosts/useHosts'
import { installFakeApi } from '../support/helpers'
import type { HostConfig, HostInput } from '../../src/shared/types'

function deferred<T>(): {
  promise: Promise<T>
  resolve: (value: T) => void
  reject: (error: Error) => void
} {
  let resolve!: (value: T) => void
  let reject!: (error: Error) => void
  const promise = new Promise<T>((yes, no) => {
    resolve = yes
    reject = no
  })
  return { promise, resolve, reject }
}

const host: HostConfig = {
  id: 'h1',
  name: 'Server',
  host: 'example.com',
  port: 22,
  username: 'root',
  authMethod: 'password'
}
const input: HostInput = {
  name: 'New',
  host: 'new.example.com',
  port: 22,
  username: 'root',
  authMethod: 'password'
}
const clients: ReturnType<typeof createAppQueryClient>[] = []

function setup(): {
  fake: ReturnType<typeof installFakeApi>
  client: ReturnType<typeof createAppQueryClient>
  wrapper: (props: { children: ReactNode }) => React.JSX.Element
} {
  const fake = installFakeApi()
  const client = createAppQueryClient()
  clients.push(client)
  const wrapper = ({ children }: { children: ReactNode }): React.JSX.Element => (
    <QueryClientProvider client={client}>{children}</QueryClientProvider>
  )
  return { fake, client, wrapper }
}

afterEach(() => {
  clients.splice(0).forEach((client) => client.clear())
  onlineManager.setOnline(true)
  focusManager.setFocused(undefined)
})

describe('host query ownership and lifecycle', () => {
  it('shares one read across consumers and keeps loading, error and empty states distinct', async () => {
    const { fake, wrapper } = setup()
    const pending = deferred<HostConfig[]>()
    fake.mocks.hosts.list.mockReturnValue(pending.promise)
    const one = renderHook(() => useHosts(), { wrapper })
    const two = renderHook(() => useHosts(), { wrapper })
    expect(one.result.current.loading).toBe(true)
    expect(one.result.current.hasData).toBe(false)
    expect(fake.mocks.hosts.list).toHaveBeenCalledTimes(1)
    await act(async () => {
      pending.reject(new Error('NODESHELL_ERR:CONFIG_READ_FAILED:unreadable'))
    })
    await waitFor(() => expect(one.result.current.error).toBe('unreadable'))
    expect(two.result.current.error).toBe('unreadable')
    expect(one.result.current.hasData).toBe(false)
    expect(fake.mocks.hosts.list).toHaveBeenCalledTimes(1)
    fake.mocks.hosts.list.mockResolvedValue([])
    await act(async () => {
      await one.result.current.refresh()
    })
    await waitFor(() => expect(two.result.current.hasData).toBe(true))
    expect(two.result.current.hosts).toEqual([])
    expect(two.result.current.error).toBeNull()
  })

  it('runs local reads while offline and never refetches on focus or reconnect', async () => {
    const { fake, wrapper } = setup()
    onlineManager.setOnline(false)
    fake.mocks.hosts.list.mockResolvedValue([host])
    const { result } = renderHook(() => useHosts(), { wrapper })
    await waitFor(() => expect(result.current.hosts).toEqual([host]))
    await act(async () => {
      focusManager.setFocused(false)
      focusManager.setFocused(true)
      onlineManager.setOnline(true)
    })
    expect(fake.mocks.hosts.list).toHaveBeenCalledTimes(1)
    await act(async () => {
      await result.current.refresh()
    })
    expect(fake.mocks.hosts.list).toHaveBeenCalledTimes(2)
  })

  it('supersedes delayed reads on consecutive refreshes', async () => {
    const { fake, wrapper } = setup()
    fake.mocks.hosts.list.mockResolvedValue([host])
    const { result } = renderHook(() => useHosts(), { wrapper })
    await waitFor(() => expect(result.current.hasData).toBe(true))
    const old = deferred<HostConfig[]>()
    const latest = deferred<HostConfig[]>()
    fake.mocks.hosts.list.mockReturnValueOnce(old.promise).mockReturnValueOnce(latest.promise)
    let first!: Promise<unknown>
    act(() => {
      first = result.current.refresh()
    })
    await waitFor(() => expect(fake.mocks.hosts.list).toHaveBeenCalledTimes(2))
    let second!: Promise<unknown>
    act(() => {
      second = result.current.refresh()
    })
    await waitFor(() => expect(fake.mocks.hosts.list).toHaveBeenCalledTimes(3))
    await act(async () => {
      latest.resolve([{ ...host, name: 'Latest' }])
      await second
    })
    await act(async () => {
      old.resolve([{ ...host, name: 'Stale' }])
      await first
    })
    await waitFor(() => expect(result.current.hosts[0].name).toBe('Latest'))
  })

  it('does not resurrect a deleted host when a pre-delete list resolves late', async () => {
    const { fake, wrapper } = setup()
    const old = deferred<HostConfig[]>()
    fake.mocks.hosts.list.mockReturnValueOnce(old.promise).mockResolvedValue([])
    fake.mocks.hosts.remove.mockResolvedValue(undefined)
    const { result } = renderHook(() => useHosts(), { wrapper })
    await act(async () => {
      await result.current.remove(host.id)
    })
    await waitFor(() => expect(result.current.hasData).toBe(true))
    expect(result.current.hosts).toEqual([])
    await act(async () => {
      old.resolve([host])
    })
    expect(result.current.hosts).toEqual([])
    expect(fake.mocks.hosts.remove).toHaveBeenCalledTimes(1)
  })

  it('refreshes credential flags authoritatively without accepting an older response', async () => {
    const { fake, wrapper } = setup()
    fake.mocks.hosts.list.mockResolvedValue([host])
    const { result } = renderHook(() => useHosts(), { wrapper })
    await waitFor(() => expect(result.current.hasData).toBe(true))
    const old = deferred<HostConfig[]>()
    fake.mocks.hosts.list.mockReturnValueOnce(old.promise)
    let oldRead!: Promise<unknown>
    act(() => {
      oldRead = result.current.refresh()
    })
    await waitFor(() => expect(fake.mocks.hosts.list).toHaveBeenCalledTimes(2))
    const saved = { ...host, credentialsSaved: true, credentialsPrompted: true }
    fake.mocks.hosts.list.mockResolvedValue([saved])
    let fresh: HostConfig[] | undefined
    await act(async () => {
      fresh = await result.current.refresh()
    })
    expect(fresh).toEqual([saved])
    await act(async () => {
      old.resolve([host])
      await oldRead
    })
    await waitFor(() => expect(result.current.hosts).toEqual([saved]))
  })

  it('keeps confirmed CRUD results when the following read fails, without retrying commands', async () => {
    const { fake, wrapper, client } = setup()
    fake.mocks.hosts.list
      .mockResolvedValueOnce([host])
      .mockRejectedValue(new Error('refresh unavailable'))
    const created = { ...input, id: 'h2' }
    fake.mocks.hosts.create.mockResolvedValue(created)
    fake.mocks.hosts.update.mockResolvedValue({ ...host, name: 'Renamed' })
    fake.mocks.hosts.remove.mockResolvedValue(undefined)
    const { result } = renderHook(() => useHosts(), { wrapper })
    await waitFor(() => expect(result.current.hasData).toBe(true))
    let returned: HostConfig | undefined
    await act(async () => {
      returned = await result.current.create(input)
    })
    expect(returned).toEqual(created)
    await waitFor(() => expect(result.current.error).toBe('refresh unavailable'))
    expect(result.current.hosts).toEqual([host, created])
    await act(async () => {
      await result.current.update('h1', { name: 'Renamed' })
    })
    await waitFor(() => expect(result.current.hosts[0].name).toBe('Renamed'))
    await act(async () => {
      await result.current.remove('h2')
    })
    await waitFor(() => expect(result.current.hosts).toHaveLength(1))
    expect(fake.mocks.hosts.create).toHaveBeenCalledTimes(1)
    expect(fake.mocks.hosts.update).toHaveBeenCalledTimes(1)
    expect(fake.mocks.hosts.remove).toHaveBeenCalledTimes(1)
    expect(client.getMutationCache().getAll()).toEqual([])
    expect(JSON.stringify(client.getQueryData(HOSTS_QUERY_KEY))).not.toContain('password":')
  })

  it('rejects failed commands once without changing confirmed data or issuing a refresh', async () => {
    const { fake, wrapper } = setup()
    fake.mocks.hosts.list.mockResolvedValue([host])
    fake.mocks.hosts.create.mockRejectedValue(new Error('create denied'))
    fake.mocks.hosts.update.mockRejectedValue(new Error('update denied'))
    fake.mocks.hosts.remove.mockRejectedValue(new Error('remove denied'))
    const { result } = renderHook(() => useHosts(), { wrapper })
    await waitFor(() => expect(result.current.hasData).toBe(true))
    await expect(result.current.create(input)).rejects.toThrow('create denied')
    await expect(result.current.update('h1', input)).rejects.toThrow('update denied')
    await expect(result.current.remove('h1')).rejects.toThrow('remove denied')
    expect(result.current.hosts).toEqual([host])
    expect(result.current.error).toBeNull()
    expect(fake.mocks.hosts.list).toHaveBeenCalledTimes(1)
  })

  it('ignores list and command completions after the owner unmounts', async () => {
    const { fake, wrapper, client } = setup()
    const list = deferred<HostConfig[]>()
    const write = deferred<HostConfig>()
    fake.mocks.hosts.list.mockReturnValue(list.promise)
    fake.mocks.hosts.create.mockReturnValue(write.promise)
    const view = renderHook(() => useHosts(), { wrapper })
    const refresh = view.result.current.refresh
    const command = view.result.current.create(input)
    view.unmount()
    await act(async () => {
      list.resolve([host])
      write.resolve({ ...input, id: 'h2' })
      await command
      await refresh()
    })
    expect(client.getQueryData(HOSTS_QUERY_KEY)).toBeUndefined()
    expect(fake.mocks.hosts.list).toHaveBeenCalledTimes(1)
  })

  it('isolates application remounts and survives StrictMode effect replay', async () => {
    const fake = installFakeApi()
    fake.mocks.hosts.list.mockResolvedValue([host])
    let client!: ReturnType<typeof createAppQueryClient>
    function Probe(): React.JSX.Element {
      client = useQueryClient()
      const { hosts } = useHosts()
      return <span>{hosts[0]?.name ?? 'loading'}</span>
    }
    const view = render(
      <StrictMode>
        <AppQueryProvider>
          <Probe />
        </AppQueryProvider>
      </StrictMode>
    )
    await waitFor(() => expect(view.getByText('Server')).toBeInTheDocument())
    const oldClient = client
    view.unmount()
    await act(async () => undefined)
    expect(oldClient.getQueryCache().getAll()).toEqual([])
    fake.mocks.hosts.list.mockResolvedValue([{ ...host, name: 'New app' }])
    const next = render(
      <AppQueryProvider>
        <Probe />
      </AppQueryProvider>
    )
    await waitFor(() => expect(next.getByText('New app')).toBeInTheDocument())
    expect(client).not.toBe(oldClient)
    next.unmount()
  })
})

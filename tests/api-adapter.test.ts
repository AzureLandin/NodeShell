import { afterEach, describe, expect, it, vi } from 'vitest'
import { createApi } from '../src/renderer/src/api/adapter'
import { WailsBridge } from '../src/renderer/src/api/bridge'
import type { ApiBridge } from '../src/renderer/src/api/bridge'

/**
 * FakeBridge records every binding call and lets tests emit runtime events.
 * It never touches window.*, so contract tests run headlessly in node.
 */
class FakeBridge implements ApiBridge {
  calls: Array<{ method: string; args: unknown[] }> = []
  listeners = new Map<string, Set<(payload: unknown) => void>>()
  private next: unknown = undefined
  private throwOn = new Set<string>()

  async call<T = unknown>(method: string, ...args: unknown[]): Promise<T> {
    this.calls.push({ method, args })
    if (this.throwOn.has(method)) {
      throw Object.assign(new Error(`NOT_IMPLEMENTED: ${method}`), {
        code: 'NOT_IMPLEMENTED'
      })
    }
    return this.next as T
  }

  setResult<T>(value: T): this {
    this.next = value
    return this
  }

  fail(method: string): this {
    this.throwOn.add(method)
    return this
  }

  on<T = unknown>(event: string, cb: (payload: T) => void): () => void {
    let set = this.listeners.get(event)
    if (!set) {
      set = new Set()
      this.listeners.set(event, set)
    }
    set.add(cb as (payload: unknown) => void)
    let off = false
    return () => {
      if (off) return
      off = true
      set!.delete(cb as (payload: unknown) => void)
    }
  }

  emit(event: string, payload: unknown): void {
    for (const cb of [...(this.listeners.get(event) ?? [])]) cb(payload)
  }

  getPathForFile(file: File): string {
    const path = (file as File & { path?: string }).path
    if (typeof path !== 'string' || path.length === 0) {
      throw Object.assign(new Error('NOT_IMPLEMENTED: getPathForFile'), {
        name: 'NotImplementedError',
        code: 'NOT_IMPLEMENTED'
      })
    }
    return path
  }
}

describe('api adapter shape', () => {
  it('exposes every ElectronApi group with its methods', () => {
    const api = createApi(new FakeBridge())
    expect(typeof api.hosts.list).toBe('function')
    expect(typeof api.hosts.create).toBe('function')
    expect(typeof api.hosts.update).toBe('function')
    expect(typeof api.hosts.remove).toBe('function')

    expect(typeof api.sessions.connect).toBe('function')
    expect(typeof api.sessions.write).toBe('function')
    expect(typeof api.sessions.resize).toBe('function')
    expect(typeof api.sessions.disconnect).toBe('function')
    expect(typeof api.sessions.cancelConnect).toBe('function')
    expect(typeof api.sessions.onData).toBe('function')
    expect(typeof api.sessions.onClosed).toBe('function')
    expect(typeof api.sessions.onError).toBe('function')

    expect(typeof api.settings.get).toBe('function')
    expect(typeof api.settings.set).toBe('function')

    expect(typeof api.credentials.isAvailable).toBe('function')
    expect(typeof api.credentials.save).toBe('function')
    expect(typeof api.credentials.clear).toBe('function')
    expect(typeof api.credentials.markPrompted).toBe('function')

    expect(typeof api.sftp.list).toBe('function')
    expect(typeof api.sftp.cwd).toBe('function')
    expect(typeof api.sftp.chdir).toBe('function')
    expect(typeof api.sftp.mkdir).toBe('function')
    expect(typeof api.sftp.rename).toBe('function')
    expect(typeof api.sftp.remove).toBe('function')
    expect(typeof api.sftp.upload).toBe('function')
    expect(typeof api.sftp.uploadPaths).toBe('function')
    expect(typeof api.sftp.download).toBe('function')
    expect(typeof api.sftp.readText).toBe('function')
    expect(typeof api.sftp.writeText).toBe('function')
    expect(typeof api.sftp.onTransferProgress).toBe('function')

    expect(typeof api.files.getPathForFile).toBe('function')

    expect(typeof api.monitor.setActive).toBe('function')
    expect(typeof api.monitor.onUpdate).toBe('function')

    expect(typeof api.tunnels.discover).toBe('function')
    expect(typeof api.tunnels.start).toBe('function')
    expect(typeof api.tunnels.stop).toBe('function')
    expect(typeof api.tunnels.list).toBe('function')

    expect(typeof api.agent!.status).toBe('function')
    expect(typeof api.agent!.upsertProvider).toBe('function')
    expect(typeof api.agent!.deleteProvider).toBe('function')
    expect(typeof api.agent!.setProviderKey).toBe('function')
    expect(typeof api.agent!.setDefaultModel).toBe('function')
    expect(typeof api.agent!.prompt).toBe('function')
    expect(typeof api.agent!.abort).toBe('function')
    expect(typeof api.agent!.clear).toBe('function')
    expect(typeof api.agent!.onDelta).toBe('function')
    expect(typeof api.agent!.onTool).toBe('function')
    expect(typeof api.agent!.onDone).toBe('function')
    expect(typeof api.agent!.onError).toBe('function')

    expect(typeof api.permission.decide).toBe('function')
    expect(typeof api.permission.onAsk).toBe('function')
    expect(typeof api.permission.onClosed).toBe('function')

    expect(typeof api.fonts.list).toBe('function')
    expect(typeof api.app.getVersion).toBe('function')
    expect(typeof api.app.openExternal).toBe('function')
    expect(typeof api.mcpRegistration.status).toBe('function')
    expect(typeof api.mcpRegistration.register).toBe('function')
    expect(typeof api.mcpRegistration.clipboardSnippet).toBe('function')
    expect(typeof api.mcpRegistration.manualConfig).toBe('function')
    expect(typeof api.dialog.openPrivateKeyFile).toBe('function')
  })
})

describe('api adapter binding dispatch', () => {
  it('maps hosts methods onto Go bindings', async () => {
    const bridge = new FakeBridge()
    const api = createApi(bridge)
    const host = { id: 'h1', name: 'n' }

    bridge.setResult([host])
    await expect(api.hosts.list()).resolves.toEqual([host])
    expect(bridge.calls.at(-1)).toEqual({ method: 'HostsList', args: [] })

    bridge.setResult(host)
    const input = { name: 'n', host: 'h', port: 22, username: 'u', authMethod: 'password' as const }
    await expect(api.hosts.create(input)).resolves.toEqual(host)
    expect(bridge.calls.at(-1)).toEqual({ method: 'HostsCreate', args: [input] })

    bridge.setResult(host)
    await expect(api.hosts.update('h1', { name: 'x' })).resolves.toEqual(host)
    expect(bridge.calls.at(-1)).toEqual({ method: 'HostsUpdate', args: ['h1', { name: 'x' }] })

    bridge.setResult(undefined)
    await api.hosts.remove('h1')
    expect(bridge.calls.at(-1)).toEqual({ method: 'HostsRemove', args: ['h1'] })
  })

  it('maps sessions methods onto Go bindings', async () => {
    const bridge = new FakeBridge()
    const api = createApi(bridge)

    bridge.setResult({ sessionId: 's1' })
    await expect(
      api.sessions.connect('h1', { password: 'pw', acceptHostKey: true })
    ).resolves.toEqual({ sessionId: 's1' })
    expect(bridge.calls.at(-1)).toEqual({
      method: 'SessionsConnect',
      args: ['h1', { password: 'pw', acceptHostKey: true }]
    })

    api.sessions.write('s1', 'ls\n')
    expect(bridge.calls.at(-1)).toEqual({ method: 'SessionsWrite', args: ['s1', 'ls\n'] })

    bridge.setResult(undefined)
    await api.sessions.resize('s1', 80, 24)
    expect(bridge.calls.at(-1)).toEqual({ method: 'SessionsResize', args: ['s1', 80, 24] })

    await api.sessions.disconnect('s1')
    expect(bridge.calls.at(-1)).toEqual({ method: 'SessionsDisconnect', args: ['s1'] })

    await api.sessions.cancelConnect()
    expect(bridge.calls.at(-1)).toEqual({ method: 'SessionsCancelConnect', args: [] })
  })

  it('maps settings, credentials, sftp, monitor, fonts, app, mcpRegistration, dialog onto bindings', async () => {
    const bridge = new FakeBridge()
    const api = createApi(bridge)

    const settings = { language: 'en' as const }
    bridge.setResult(settings)
    await expect(api.settings.get()).resolves.toEqual(settings)
    expect(bridge.calls.at(-1)).toEqual({ method: 'SettingsGet', args: [] })
    await expect(api.settings.set({ language: 'zh' })).resolves.toEqual(settings)
    expect(bridge.calls.at(-1)).toEqual({ method: 'SettingsSet', args: [{ language: 'zh' }] })

    bridge.setResult(true)
    await expect(api.credentials.isAvailable()).resolves.toBe(true)
    expect(bridge.calls.at(-1)).toEqual({ method: 'CredentialsIsAvailable', args: [] })
    bridge.setResult(undefined)
    await api.credentials.save('h1', { password: 'pw' })
    expect(bridge.calls.at(-1)).toEqual({
      method: 'CredentialsSave',
      args: ['h1', { password: 'pw' }]
    })
    await api.credentials.clear('h1')
    expect(bridge.calls.at(-1)).toEqual({ method: 'CredentialsClear', args: ['h1'] })
    await api.credentials.markPrompted('h1', true)
    expect(bridge.calls.at(-1)).toEqual({ method: 'CredentialsMarkPrompted', args: ['h1', true] })

    const entries = [{ name: 'a', path: '/a', isDirectory: true, size: 0, modifyTime: 1 }]
    bridge.setResult(entries)
    await expect(api.sftp.list('s1')).resolves.toEqual(entries)
    expect(bridge.calls.at(-1)).toEqual({ method: 'SftpList', args: ['s1'] })
    bridge.setResult('/home/u')
    await expect(api.sftp.cwd('s1')).resolves.toBe('/home/u')
    expect(bridge.calls.at(-1)).toEqual({ method: 'SftpCwd', args: ['s1'] })
    await expect(api.sftp.chdir('s1', '/tmp')).resolves.toBe('/home/u')
    expect(bridge.calls.at(-1)).toEqual({ method: 'SftpChdir', args: ['s1', '/tmp'] })
    bridge.setResult(undefined)
    await api.sftp.mkdir('s1', 'dir')
    expect(bridge.calls.at(-1)).toEqual({ method: 'SftpMkdir', args: ['s1', 'dir'] })
    await api.sftp.rename('s1', 'a', 'b')
    expect(bridge.calls.at(-1)).toEqual({ method: 'SftpRename', args: ['s1', 'a', 'b'] })
    await api.sftp.remove('s1', '/tmp/x')
    expect(bridge.calls.at(-1)).toEqual({ method: 'SftpRemove', args: ['s1', '/tmp/x'] })
    await api.sftp.upload('s1')
    expect(bridge.calls.at(-1)).toEqual({ method: 'SftpUpload', args: ['s1'] })
    await api.sftp.uploadPaths('s1', ['/a', '/b'])
    expect(bridge.calls.at(-1)).toEqual({ method: 'SftpUploadPaths', args: ['s1', ['/a', '/b']] })
    await api.sftp.download('s1', '/a', 'a')
    expect(bridge.calls.at(-1)).toEqual({ method: 'SftpDownload', args: ['s1', '/a', 'a'] })
    bridge.setResult({ path: '/a', content: 'hi' })
    await expect(api.sftp.readText('s1', '/a')).resolves.toEqual({ path: '/a', content: 'hi' })
    expect(bridge.calls.at(-1)).toEqual({ method: 'SftpReadText', args: ['s1', '/a'] })
    bridge.setResult({ path: '/a' })
    await expect(api.sftp.writeText('s1', '/a', 'hi')).resolves.toEqual({ path: '/a' })
    expect(bridge.calls.at(-1)).toEqual({ method: 'SftpWriteText', args: ['s1', '/a', 'hi'] })

    bridge.setResult(undefined)
    await api.monitor.setActive('s1', 't')
    expect(bridge.calls.at(-1)).toEqual({ method: 'MonitorSetActive', args: ['s1', 't'] })

    await api.tunnels.discover('s1')
    expect(bridge.calls.at(-1)).toEqual({ method: 'TunnelsDiscover', args: ['s1'] })
    await api.tunnels.start('s1', '0.0.0.0', 8080)
    expect(bridge.calls.at(-1)).toEqual({ method: 'TunnelsStart', args: ['s1', '0.0.0.0', 8080] })
    await api.tunnels.stop('s1', 'tun-1')
    expect(bridge.calls.at(-1)).toEqual({ method: 'TunnelsStop', args: ['s1', 'tun-1'] })
    await api.tunnels.list('s1')
    expect(bridge.calls.at(-1)).toEqual({ method: 'TunnelsList', args: ['s1'] })

    const agentStatus = {
      configured: true,
      providers: [
        { id: 'p1', name: 'P', baseUrl: 'https://x.test/v1', models: ['m'], hasKey: true }
      ],
      defaultProviderId: 'p1',
      defaultModel: 'm'
    }
    bridge.setResult(agentStatus)
    await expect(api.agent!.status()).resolves.toEqual(agentStatus)
    expect(bridge.calls.at(-1)).toEqual({ method: 'AgentStatus', args: [] })
    await expect(
      api.agent!.upsertProvider({ name: 'P', baseUrl: 'https://x.test/v1', models: ['m'] })
    ).resolves.toEqual(agentStatus)
    expect(bridge.calls.at(-1)).toEqual({
      method: 'AgentUpsertProvider',
      args: [{ name: 'P', baseUrl: 'https://x.test/v1', models: ['m'] }]
    })
    await expect(api.agent!.setProviderKey('p1', 'k')).resolves.toEqual(agentStatus)
    expect(bridge.calls.at(-1)).toEqual({ method: 'AgentSetProviderKey', args: ['p1', 'k'] })
    await expect(api.agent!.setDefaultModel('p1', 'm')).resolves.toEqual(agentStatus)
    expect(bridge.calls.at(-1)).toEqual({ method: 'AgentSetDefaultModel', args: ['p1', 'm'] })
    await expect(api.agent!.deleteProvider('p1')).resolves.toEqual(agentStatus)
    expect(bridge.calls.at(-1)).toEqual({ method: 'AgentDeleteProvider', args: ['p1'] })
    bridge.setResult(undefined)
    await api.agent!.prompt('s1', 'prod-web', 'hello', 'p1', 'm')
    expect(bridge.calls.at(-1)).toEqual({
      method: 'AgentPrompt',
      args: ['s1', 'prod-web', 'hello', 'p1', 'm']
    })
    await api.agent!.abort('s1')
    expect(bridge.calls.at(-1)).toEqual({ method: 'AgentAbort', args: ['s1'] })
    await api.agent!.clear('s1')
    expect(bridge.calls.at(-1)).toEqual({ method: 'AgentClear', args: ['s1'] })

    await api.permission.decide('ask-1', 'allow-session')
    expect(bridge.calls.at(-1)).toEqual({
      method: 'PermissionDecide',
      args: ['ask-1', 'allow-session']
    })

    bridge.setResult(['mono'])
    await expect(api.fonts.list()).resolves.toEqual(['mono'])
    expect(bridge.calls.at(-1)).toEqual({ method: 'FontsList', args: [] })

    bridge.setResult('2.0.0')
    await expect(api.app.getVersion()).resolves.toBe('2.0.0')
    expect(bridge.calls.at(-1)).toEqual({ method: 'AppGetVersion', args: [] })
    bridge.setResult(undefined)
    await api.app.openExternal!('https://example.com')
    expect(bridge.calls.at(-1)).toEqual({
      method: 'AppOpenExternal',
      args: ['https://example.com']
    })

    const statuses: never[] = []
    bridge.setResult(statuses)
    await expect(api.mcpRegistration.status()).resolves.toEqual(statuses)
    expect(bridge.calls.at(-1)).toEqual({ method: 'McpRegistrationStatus', args: [] })
    await api.mcpRegistration.register('cursor')
    expect(bridge.calls.at(-1)).toEqual({ method: 'McpRegistrationRegister', args: ['cursor'] })
    bridge.setResult('snippet')
    await expect(api.mcpRegistration.clipboardSnippet()).resolves.toBe('snippet')
    expect(bridge.calls.at(-1)).toEqual({ method: 'McpRegistrationClipboardSnippet', args: [] })
    const kit = {
      command: '/x/nodeshell',
      args: ['--mcp'],
      snippets: { standard: 's', vscode: 'v', opencode: 'o', codex: 'c' }
    }
    bridge.setResult(kit)
    await expect(api.mcpRegistration.manualConfig()).resolves.toEqual(kit)
    expect(bridge.calls.at(-1)).toEqual({ method: 'McpRegistrationManualConfig', args: [] })

    bridge.setResult('/key')
    await expect(api.dialog.openPrivateKeyFile()).resolves.toBe('/key')
    expect(bridge.calls.at(-1)).toEqual({ method: 'DialogOpenPrivateKeyFile', args: [] })
  })

  it('exposes the injected bridge result for files.getPathForFile', () => {
    const bridge = new FakeBridge()
    const api = createApi(bridge)
    const file = { name: 'f.txt', path: 'C:\\tmp\\f.txt' } as unknown as File
    expect(api.files.getPathForFile(file)).toBe('C:\\tmp\\f.txt')
  })

  it('maps files.onDrop onto the files:onDrop event with absolute paths', () => {
    const bridge = new FakeBridge()
    const api = createApi(bridge)
    const cb = vi.fn()
    const off = api.files.onDrop!(cb)

    bridge.emit('files:onDrop', { paths: ['C:\\a.txt', 'D:\\b.txt'] })
    expect(cb).toHaveBeenCalledTimes(1)
    expect(cb).toHaveBeenCalledWith(['C:\\a.txt', 'D:\\b.txt'])

    // Unsubscribe stops delivery; the second call is a no-op.
    off()
    off()
    bridge.emit('files:onDrop', { paths: ['C:\\c.txt'] })
    expect(cb).toHaveBeenCalledTimes(1)
  })

  it('files.onDrop returns an idempotent unsubscribe shared with other subscribers', () => {
    const bridge = new FakeBridge()
    const api = createApi(bridge)
    const a = vi.fn()
    const b = vi.fn()
    const offA = api.files.onDrop!(a)
    api.files.onDrop!(b)

    bridge.emit('files:onDrop', { paths: ['/x'] })
    expect(a).toHaveBeenCalledTimes(1)
    expect(b).toHaveBeenCalledTimes(1)

    offA()
    bridge.emit('files:onDrop', { paths: ['/y'] })
    expect(a).toHaveBeenCalledTimes(1)
    expect(b).toHaveBeenCalledTimes(2)
  })
})

describe('api adapter event mapping', () => {
  it('maps sessions onData/onClosed/onError onto session events', () => {
    const bridge = new FakeBridge()
    const api = createApi(bridge)
    const data = vi.fn()
    const closed = vi.fn()
    const error = vi.fn()

    api.sessions.onData(data)
    api.sessions.onClosed(closed)
    api.sessions.onError(error)

    bridge.emit('session:data', { sessionId: 's1', data: 'x' })
    bridge.emit('session:closed', { sessionId: 's1' })
    bridge.emit('session:error', { sessionId: 's1', error: { code: 'TIMEOUT', message: 't' } })

    expect(data).toHaveBeenCalledWith({ sessionId: 's1', data: 'x' })
    expect(closed).toHaveBeenCalledWith({ sessionId: 's1' })
    expect(error).toHaveBeenCalledWith({
      sessionId: 's1',
      error: { code: 'TIMEOUT', message: 't' }
    })
  })

  it('maps sftp transfer progress and monitor update events', () => {
    const bridge = new FakeBridge()
    const api = createApi(bridge)
    const progress = vi.fn()
    const update = vi.fn()

    api.sftp.onTransferProgress(progress)
    api.monitor.onUpdate(update)

    bridge.emit('sftp:transferProgress', {
      sessionId: 's1',
      direction: 'up',
      name: 'f',
      transferred: 1,
      total: 2,
      done: false
    })
    bridge.emit('monitor:update', { sessionId: 's1', snapshot: null })

    expect(progress).toHaveBeenCalledWith({
      sessionId: 's1',
      direction: 'up',
      name: 'f',
      transferred: 1,
      total: 2,
      done: false
    })
    expect(update).toHaveBeenCalledWith({ sessionId: 's1', snapshot: null })
  })

  it('maps the four agent events onto their runtime channels', () => {
    const bridge = new FakeBridge()
    const api = createApi(bridge)
    const delta = vi.fn()
    const tool = vi.fn()
    const done = vi.fn()
    const failed = vi.fn()

    api.agent!.onDelta(delta)
    api.agent!.onTool(tool)
    api.agent!.onDone(done)
    api.agent!.onError(failed)

    bridge.emit('agent:delta', { sessionId: 's1', delta: 'hi' })
    bridge.emit('agent:tool', {
      sessionId: 's1',
      callId: 'c1',
      name: 'bash',
      summary: 'df -h',
      ok: true
    })
    bridge.emit('agent:done', { sessionId: 's1', aborted: false })
    bridge.emit('agent:error', {
      sessionId: 's1',
      error: { code: 'UNKNOWN', message: 'boom' }
    })

    expect(delta).toHaveBeenCalledWith({ sessionId: 's1', delta: 'hi' })
    expect(tool).toHaveBeenCalledWith({
      sessionId: 's1',
      callId: 'c1',
      name: 'bash',
      summary: 'df -h',
      ok: true
    })
    expect(done).toHaveBeenCalledWith({ sessionId: 's1', aborted: false })
    expect(failed).toHaveBeenCalledWith({
      sessionId: 's1',
      error: { code: 'UNKNOWN', message: 'boom' }
    })
  })

  it('maps permission ask/closed onto their runtime channels', () => {
    const bridge = new FakeBridge()
    const api = createApi(bridge)
    const asked = vi.fn()
    const closed = vi.fn()
    api.permission.onAsk(asked)
    api.permission.onClosed(closed)

    const payload = {
      id: 'ask-1',
      source: 'agent',
      tool: 'bash',
      sessionId: 's1',
      title: 'prod-web',
      summary: 'uptime'
    }
    bridge.emit('permission:ask', payload)
    bridge.emit('permission:closed', { id: 'ask-1' })
    expect(asked).toHaveBeenCalledWith(payload)
    expect(closed).toHaveBeenCalledWith({ id: 'ask-1' })
  })

  it('returns idempotent unsubscribe; callbacks stop after unsubscribe', () => {
    const bridge = new FakeBridge()
    const api = createApi(bridge)
    const a = vi.fn()
    const b = vi.fn()
    const offA = api.sessions.onData(a)
    api.sessions.onData(b)

    bridge.emit('session:data', { sessionId: 's1', data: '1' })
    expect(a).toHaveBeenCalledTimes(1)
    expect(b).toHaveBeenCalledTimes(1)

    offA()
    offA() // second call must be a no-op
    bridge.emit('session:data', { sessionId: 's1', data: '2' })
    expect(a).toHaveBeenCalledTimes(1)
    expect(b).toHaveBeenCalledTimes(2)
  })

  it('subscribers on the same event stay isolated when another unsubscribes', () => {
    const bridge = new FakeBridge()
    const api = createApi(bridge)
    const a = vi.fn()
    const b = vi.fn()
    const offA = api.sessions.onError(a)
    const offB = api.sessions.onError(b)

    offA()
    bridge.emit('session:error', { sessionId: 's1', error: { code: 'UNKNOWN', message: 'x' } })
    expect(a).not.toHaveBeenCalled()
    expect(b).toHaveBeenCalledTimes(1)
    offB()
  })
})

describe('api adapter observable failures', () => {
  it('propagates NOT_IMPLEMENTED from the bridge as a rejected promise', async () => {
    const bridge = new FakeBridge().fail('HostsList')
    const api = createApi(bridge)
    await expect(api.hosts.list()).rejects.toMatchObject({ code: 'NOT_IMPLEMENTED' })
  })

  it('write is fire-and-forget and never rejects even when the binding is missing', () => {
    const bridge = new FakeBridge().fail('SessionsWrite')
    const api = createApi(bridge)
    expect(() => api.sessions.write('s1', 'ls\n')).not.toThrow()
  })
})

describe('WailsBridge against a bare node environment', () => {
  it('fails explicitly with NOT_IMPLEMENTED when generated bindings are absent', async () => {
    const bridge = new WailsBridge()
    await expect(bridge.call('HostsList')).rejects.toMatchObject({ code: 'NOT_IMPLEMENTED' })
  })

  it('on() degrades to a safe no-op unsubscribe when runtime is absent', () => {
    const bridge = new WailsBridge()
    const off = bridge.on('session:data', () => undefined)
    expect(typeof off).toBe('function')
    expect(() => off()).not.toThrow()
    expect(() => off()).not.toThrow()
  })
})

describe('WailsBridge.getPathForFile', () => {
  it('returns the explicit path when the File carries one (Electron/webUtils compat)', () => {
    const bridge = new WailsBridge()
    const file = { name: 'f.txt', path: 'C:\\tmp\\f.txt' } as unknown as File
    expect(bridge.getPathForFile(file)).toBe('C:\\tmp\\f.txt')
  })

  it('throws NOT_IMPLEMENTED for a plain DOM File with no real path (Wails drop has none)', () => {
    const bridge = new WailsBridge()
    const file = { name: 'f.txt' } as File
    expect(() => bridge.getPathForFile(file)).toThrowError(
      expect.objectContaining({ code: 'NOT_IMPLEMENTED' })
    )
  })
})

describe('WailsBridge event unsubscribe', () => {
  const originalWindow = globalThis.window

  afterEach(() => {
    if (originalWindow === undefined) {
      delete (globalThis as { window?: unknown }).window
    } else {
      ;(globalThis as { window: unknown }).window = originalWindow
    }
  })

  function installRuntime(runtime: unknown): void {
    ;(globalThis as { window?: unknown }).window = { runtime }
  }

  it('unsubscribes via the per-listener off returned by EventsOn, not EventsOff', () => {
    const eventsOff = vi.fn()
    const perListenerOff = vi.fn()
    installRuntime({
      EventsOn: vi.fn(() => perListenerOff),
      EventsOff: eventsOff
    })
    const bridge = new WailsBridge()
    const off = bridge.on('session:data', () => undefined)
    off()
    expect(perListenerOff).toHaveBeenCalledTimes(1)
    expect(eventsOff).not.toHaveBeenCalled()
  })

  it('unsubscribe is idempotent and never leaks a second off call', () => {
    const eventsOff = vi.fn()
    const perListenerOff = vi.fn()
    installRuntime({
      EventsOn: vi.fn(() => perListenerOff),
      EventsOff: eventsOff
    })
    const bridge = new WailsBridge()
    const off = bridge.on('session:data', () => undefined)
    off()
    off()
    expect(perListenerOff).toHaveBeenCalledTimes(1)
    expect(eventsOff).not.toHaveBeenCalled()
  })

  it('falls back to EventsOff when EventsOn returns no unsubscribe (older runtimes)', () => {
    const eventsOff = vi.fn()
    installRuntime({
      EventsOn: vi.fn(() => undefined),
      EventsOff: eventsOff
    })
    const bridge = new WailsBridge()
    const off = bridge.on('session:data', () => undefined)
    off()
    expect(eventsOff).toHaveBeenCalledTimes(1)
    expect(eventsOff).toHaveBeenCalledWith('session:data')
  })
})

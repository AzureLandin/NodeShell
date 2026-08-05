import {
  IPC,
  type AppSettings,
  type ConnectOptions,
  type ElectronApi,
  type HostConfig,
  type HostInput,
  type McpRegistrationResult,
  type McpRegistrationTarget,
  type McpRegistrationTargetStatus,
  type MonitorUpdateEvent,
  type SessionClosedEvent,
  type SessionDataEvent,
  type SessionErrorEvent,
  type SftpTransferProgressEvent
} from '../../../shared/types'
import type { ApiBridge } from './bridge'

/** Shape of an SFTP directory entry (matches ElectronApi['sftp']['list']). */
export interface SftpEntry {
  name: string
  path: string
  isDirectory: boolean
  size: number
  modifyTime: number
}

/**
 * Wails adapter: exposes the same window.api groups/shapes as the Electron
 * preload, backed by an injectable ApiBridge. Binding method names follow the
 * convention the Go App facade implements: `<Group><Method>` in PascalCase
 * (e.g. hosts.list -> HostsList, mcpRegistration.status -> McpRegistrationStatus).
 *
 * Event names reuse the shared IPC constants so Go emits the same strings.
 * Each onX returns an idempotent unsubscribe; the last subscriber for an
 * event tears down the single bridge listener.
 */

/**
 * Fans a single bridge listener out to N subscribers with idempotent off().
 * The bridge listener is created lazily on first subscribe and torn down when
 * the last subscriber leaves.
 */
class EventMultiplexer {
  private readonly events = new Map<
    string,
    { bridgeOff: () => void; subs: Set<(payload: unknown) => void> }
  >()

  constructor(private readonly bridge: ApiBridge) {}

  on<T>(event: string, cb: (payload: T) => void): () => void {
    let entry = this.events.get(event)
    if (!entry) {
      const subs = new Set<(payload: unknown) => void>()
      const bridgeOff = this.bridge.on(event, (payload) => {
        for (const sub of subs) sub(payload)
      })
      entry = { bridgeOff, subs }
      this.events.set(event, entry)
    }
    const sub = cb as (payload: unknown) => void
    entry.subs.add(sub)
    let cancelled = false
    return () => {
      if (cancelled) return
      cancelled = true
      const current = this.events.get(event)
      if (!current) return
      current.subs.delete(sub)
      if (current.subs.size === 0) {
        current.bridgeOff()
        this.events.delete(event)
      }
    }
  }
}

export function createApi(bridge: ApiBridge): ElectronApi {
  const events = new EventMultiplexer(bridge)

  return {
    hosts: {
      list: (): Promise<HostConfig[]> => bridge.call<HostConfig[]>('HostsList'),
      create: (input: HostInput): Promise<HostConfig> =>
        bridge.call<HostConfig>('HostsCreate', input),
      update: (id: string, patch: Partial<HostInput>): Promise<HostConfig> =>
        bridge.call<HostConfig>('HostsUpdate', id, patch),
      remove: (id: string): Promise<void> => bridge.call<void>('HostsRemove', id)
    },
    sessions: {
      connect: (hostId: string, options?: ConnectOptions): Promise<{ sessionId: string }> =>
        bridge.call<{ sessionId: string }>('SessionsConnect', hostId, options),
      // Terminal input is fire-and-forget: never surface as an unhandled
      // rejection, but keep the failure observable in the console.
      write: (sessionId: string, data: string): void => {
        bridge.call('SessionsWrite', sessionId, data).catch((err: unknown) => {
          console.warn('[api] sessions.write failed', err)
        })
      },
      resize: (sessionId: string, cols: number, rows: number): Promise<void> =>
        bridge.call<void>('SessionsResize', sessionId, cols, rows),
      disconnect: (sessionId: string): Promise<void> =>
        bridge.call<void>('SessionsDisconnect', sessionId),
      cancelConnect: (): Promise<void> => bridge.call<void>('SessionsCancelConnect'),
      onData: (cb: (event: SessionDataEvent) => void): (() => void) =>
        events.on<SessionDataEvent>(IPC.sessionData, cb),
      onClosed: (cb: (event: SessionClosedEvent) => void): (() => void) =>
        events.on<SessionClosedEvent>(IPC.sessionClosed, cb),
      onError: (cb: (event: SessionErrorEvent) => void): (() => void) =>
        events.on<SessionErrorEvent>(IPC.sessionError, cb)
    },
    settings: {
      get: (): Promise<AppSettings> => bridge.call<AppSettings>('SettingsGet'),
      set: (patch: Partial<AppSettings>): Promise<AppSettings> =>
        bridge.call<AppSettings>('SettingsSet', patch)
    },
    credentials: {
      isAvailable: (): Promise<boolean> => bridge.call<boolean>('CredentialsIsAvailable'),
      save: (
        hostId: string,
        payload: { password?: string; privateKeyPath?: string }
      ): Promise<void> => bridge.call<void>('CredentialsSave', hostId, payload),
      clear: (hostId: string): Promise<void> => bridge.call<void>('CredentialsClear', hostId),
      markPrompted: (hostId: string, saved: boolean): Promise<void> =>
        bridge.call<void>('CredentialsMarkPrompted', hostId, saved)
    },
    sftp: {
      list: (sessionId: string): Promise<SftpEntry[]> =>
        bridge.call<SftpEntry[]>('SftpList', sessionId),
      cwd: (sessionId: string): Promise<string> => bridge.call<string>('SftpCwd', sessionId),
      chdir: (sessionId: string, remotePath: string): Promise<string> =>
        bridge.call<string>('SftpChdir', sessionId, remotePath),
      mkdir: (sessionId: string, name: string): Promise<void> =>
        bridge.call<void>('SftpMkdir', sessionId, name),
      rename: (sessionId: string, from: string, to: string): Promise<void> =>
        bridge.call<void>('SftpRename', sessionId, from, to),
      remove: (sessionId: string, remotePath: string): Promise<void> =>
        bridge.call<void>('SftpRemove', sessionId, remotePath),
      upload: (sessionId: string): Promise<void> => bridge.call<void>('SftpUpload', sessionId),
      uploadPaths: (sessionId: string, localPaths: string[]): Promise<void> =>
        bridge.call<void>('SftpUploadPaths', sessionId, localPaths),
      download: (sessionId: string, remotePath: string, defaultName: string): Promise<void> =>
        bridge.call<void>('SftpDownload', sessionId, remotePath, defaultName),
      readText: (
        sessionId: string,
        remotePath: string
      ): Promise<{ path: string; content: string }> =>
        bridge.call<{ path: string; content: string }>('SftpReadText', sessionId, remotePath),
      writeText: (
        sessionId: string,
        remotePath: string,
        content: string
      ): Promise<{ path: string }> =>
        bridge.call<{ path: string }>('SftpWriteText', sessionId, remotePath, content),
      onTransferProgress: (cb: (event: SftpTransferProgressEvent) => void): (() => void) =>
        events.on<SftpTransferProgressEvent>(IPC.sftpTransferProgress, cb)
    },
    files: {
      getPathForFile: (file: File): string => bridge.getPathForFile(file),
      // Native file drops (Wails OnFileDrop) arrive as an event carrying the
      // absolute paths; the SftpPanel associates them with its current
      // session. Only the Wails adapter exposes this — the Electron preload
      // keeps the DOM drop path.
      onDrop: (cb: (paths: string[]) => void): (() => void) =>
        events.on<{ paths: string[] }>(IPC.filesOnDrop, (payload) => cb(payload.paths))
    },
    monitor: {
      setActive: (sessionId: string | null, title?: string): Promise<void> =>
        bridge.call<void>('MonitorSetActive', sessionId, title),
      onUpdate: (cb: (event: MonitorUpdateEvent) => void): (() => void) =>
        events.on<MonitorUpdateEvent>(IPC.monitorUpdate, cb)
    },
    fonts: {
      list: (): Promise<string[]> => bridge.call<string[]>('FontsList')
    },
    app: {
      getVersion: (): Promise<string> => bridge.call<string>('AppGetVersion')
    },
    mcpRegistration: {
      status: (): Promise<McpRegistrationTargetStatus[]> =>
        bridge.call<McpRegistrationTargetStatus[]>('McpRegistrationStatus'),
      register: (target: McpRegistrationTarget | 'all'): Promise<McpRegistrationResult[]> =>
        bridge.call<McpRegistrationResult[]>('McpRegistrationRegister', target),
      clipboardSnippet: (): Promise<string> =>
        bridge.call<string>('McpRegistrationClipboardSnippet')
    },
    dialog: {
      openPrivateKeyFile: (): Promise<string | null> =>
        bridge.call<string | null>('DialogOpenPrivateKeyFile')
    }
  }
}

export { notImplementedError } from './bridge'
export type { ApiBridge, NotImplementedError } from './bridge'

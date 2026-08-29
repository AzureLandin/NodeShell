import { render } from '@testing-library/react'
import { I18nextProvider } from 'react-i18next'
import i18n from 'i18next'
import { vi } from 'vitest'
import type { ReactElement } from 'react'
import type {
  AgentConfigStatus,
  AgentDeltaEvent,
  AgentDoneEvent,
  AgentErrorEvent,
  AgentToolEvent,
  ElectronApi,
  PermissionAskEvent,
  PermissionClosedEvent,
  SessionClosedEvent,
  SessionDataEvent,
  SessionErrorEvent,
  TransferTask
} from '../../src/shared/types'
import en from '../../src/renderer/src/i18n/locales/en.json'

export const defaultAgentStatus: AgentConfigStatus = {
  configured: true,
  providers: [
    {
      id: 'p1',
      name: 'OpenAI',
      baseUrl: 'https://api.openai.com/v1',
      models: ['gpt-4o-mini', 'gpt-4o'],
      hasKey: true
    }
  ],
  defaultProviderId: 'p1',
  defaultModel: 'gpt-4o-mini'
}

/**
 * Shared T1.8.3 UI-test plumbing: a fake window.api recording every call, and
 * an i18n wrapper using the real en translations (so label queries track the
 * source of truth instead of hardcoded strings). Nothing here mocks component
 * behavior — components are rendered as-is; only window.api is faked.
 */

/** Minimal i18next instance initialised synchronously from the real en locale. */
export function createTestI18n(): ReturnType<typeof i18n.createInstance> {
  const instance = i18n.createInstance()
  instance.init({
    resources: { en: { translation: en } },
    lng: 'en',
    fallbackLng: 'en',
    initAsync: false,
    interpolation: { escapeValue: false }
  })
  return instance
}

export function renderWithI18n(ui: ReactElement): ReturnType<typeof render> {
  return render(<I18nextProvider i18n={createTestI18n()}>{ui}</I18nextProvider>)
}

type MockFns = ReturnType<typeof vi.fn>
type MockGroup<T extends object> = { [K in keyof T]-?: MockFns }

export interface ApiMocks {
  hosts: MockGroup<ElectronApi['hosts']>
  sessions: MockGroup<ElectronApi['sessions']>
  settings: MockGroup<ElectronApi['settings']>
  credentials: MockGroup<ElectronApi['credentials']>
  sftp: MockGroup<ElectronApi['sftp']>
  transfer: MockGroup<ElectronApi['transfer']>
  files: MockGroup<ElectronApi['files']>
  monitor: MockGroup<ElectronApi['monitor']>
  tunnels: MockGroup<ElectronApi['tunnels']>
  agent: MockGroup<NonNullable<ElectronApi['agent']>>
  permission: MockGroup<ElectronApi['permission']>
  fonts: MockGroup<ElectronApi['fonts']>
  app: MockGroup<ElectronApi['app']>
  mcpRegistration: MockGroup<ElectronApi['mcpRegistration']>
  dialog: MockGroup<ElectronApi['dialog']>
}

export type FakeApi = { api: ElectronApi; mocks: ApiMocks }

/** ElectronApi-shaped fake; `mocks` mirrors the api tree with vi.fn()s. */
export function createFakeApi(): FakeApi {
  const mocks = {
    hosts: { list: vi.fn(), create: vi.fn(), update: vi.fn(), remove: vi.fn() },
    sessions: {
      connect: vi.fn(),
      write: vi.fn(),
      resize: vi.fn(),
      disconnect: vi.fn(),
      cancelConnect: vi.fn(),
      onData: vi.fn(() => () => undefined),
      onClosed: vi.fn(() => () => undefined),
      onError: vi.fn(() => () => undefined)
    },
    settings: { get: vi.fn(), set: vi.fn() },
    credentials: {
      isAvailable: vi.fn(),
      save: vi.fn(),
      clear: vi.fn(),
      markPrompted: vi.fn()
    },
    sftp: {
      list: vi.fn(),
      cwd: vi.fn(),
      chdir: vi.fn(),
      mkdir: vi.fn(),
      rename: vi.fn(),
      remove: vi.fn(),
      upload: vi.fn(),
      uploadPaths: vi.fn(),
      download: vi.fn(),
      readText: vi.fn(),
      writeText: vi.fn(),
      onTransferProgress: vi.fn(() => vi.fn())
    },
    transfer: {
      getTasks: vi.fn(),
      enqueueUpload: vi.fn(),
      enqueueDownload: vi.fn(),
      chooseUploadFiles: vi.fn(),
      chooseDownloadTarget: vi.fn(),
      cancel: vi.fn(),
      retry: vi.fn(),
      clear: vi.fn(),
      clearCompleted: vi.fn(),
      onTask: vi.fn(() => vi.fn())
    },
    files: {
      getPathForFile: vi.fn(),
      onDrop: vi.fn(() => vi.fn())
    },
    monitor: { setActive: vi.fn(), onUpdate: vi.fn(() => () => undefined) },
    tunnels: {
      discover: vi.fn(),
      start: vi.fn(),
      stop: vi.fn(),
      list: vi.fn()
    },
    agent: {
      status: vi.fn(),
      upsertProvider: vi.fn(),
      deleteProvider: vi.fn(),
      setProviderKey: vi.fn(),
      setDefaultModel: vi.fn(),
      prompt: vi.fn(),
      abort: vi.fn(),
      clear: vi.fn(),
      onDelta: vi.fn(() => () => undefined),
      onTool: vi.fn(() => () => undefined),
      onDone: vi.fn(() => () => undefined),
      onError: vi.fn(() => () => undefined)
    },
    permission: {
      decide: vi.fn(),
      onAsk: vi.fn(() => () => undefined),
      onClosed: vi.fn(() => () => undefined)
    },
    fonts: { list: vi.fn() },
    app: { getVersion: vi.fn(), openExternal: vi.fn() },
    mcpRegistration: { status: vi.fn(), register: vi.fn(), clipboardSnippet: vi.fn(), manualConfig: vi.fn() },
    dialog: { openPrivateKeyFile: vi.fn() }
  }

  const api = {
    hosts: mocks.hosts,
    sessions: mocks.sessions,
    settings: mocks.settings,
    credentials: mocks.credentials,
    sftp: mocks.sftp,
    transfer: mocks.transfer,
    files: mocks.files,
    monitor: mocks.monitor,
    tunnels: mocks.tunnels,
    agent: mocks.agent,
    permission: mocks.permission,
    fonts: mocks.fonts,
    app: mocks.app,
    mcpRegistration: mocks.mcpRegistration,
    dialog: mocks.dialog
  }

  return { api, mocks }
}

export function installFakeApi(): FakeApi {
  const fake = createFakeApi()
  // Benign defaults: components must never crash on an unresolved mock.
  fake.mocks.fonts.list.mockResolvedValue([])
  fake.mocks.mcpRegistration.status.mockResolvedValue([])
  fake.mocks.mcpRegistration.manualConfig.mockResolvedValue({
    command: '/x/nodeshell',
    args: ['--mcp'],
    snippets: {
      standard: '{\n  "mcpServers": {}\n}',
      vscode: '{\n  "servers": {}\n}',
      opencode: '{\n  "mcp": {}\n}',
      codex: '[mcp_servers.nodeshell]'
    }
  })
  fake.mocks.agent.status.mockResolvedValue({ ...defaultAgentStatus })
  fake.mocks.agent.prompt.mockResolvedValue(undefined)
  fake.mocks.agent.setDefaultModel.mockResolvedValue({ ...defaultAgentStatus })
  fake.mocks.app.openExternal.mockResolvedValue(undefined)
  fake.mocks.permission.decide.mockResolvedValue(undefined)
  fake.mocks.tunnels.discover.mockResolvedValue([])
  fake.mocks.tunnels.list.mockResolvedValue([])
  fake.mocks.tunnels.start.mockResolvedValue({
    id: 'tun-1',
    sessionId: 's1',
    localHost: '127.0.0.1',
    localPort: 8080,
    remoteAddr: '0.0.0.0',
    remotePort: 8080
  })
  fake.mocks.tunnels.stop.mockResolvedValue(undefined)
  fake.mocks.transfer.getTasks.mockResolvedValue([])
  fake.mocks.transfer.enqueueUpload.mockResolvedValue([])
  fake.mocks.transfer.enqueueDownload.mockResolvedValue('task-1')
  fake.mocks.transfer.chooseUploadFiles.mockResolvedValue([])
  fake.mocks.transfer.chooseDownloadTarget.mockResolvedValue('task-1')
  fake.mocks.transfer.cancel.mockResolvedValue(undefined)
  fake.mocks.transfer.retry.mockResolvedValue('task-2')
  fake.mocks.transfer.clear.mockResolvedValue(undefined)
  fake.mocks.transfer.clearCompleted.mockResolvedValue(undefined)
  ;(window as Window & { api: unknown }).api = fake.api
  return fake
}

export function emitTransferEvent(fake: FakeApi, payload: TransferTask): void {
  const cb = fake.mocks.transfer.onTask.mock.calls[0]?.[0] as ((p: TransferTask) => void) | undefined
  if (!cb) throw new Error('emitTransferEvent: onTask never subscribed')
  cb(payload)
}

type SessionEventKind = 'data' | 'closed' | 'error'
type SessionEventPayload = SessionDataEvent | SessionClosedEvent | SessionErrorEvent

/**
 * Deliver a session event to the callback `useSessions` registered on mount.
 * Faithful to the EventMultiplexer/window.api contract: the adapter fans the
 * bridge payload out to every subscriber, and useSessions is the only
 * subscriber while App is mounted, so invoking its captured callback is
 * equivalent to the backend emitting the Wails event.
 */
export function emitSessionEvent(
  fake: FakeApi,
  kind: SessionEventKind,
  payload: SessionEventPayload
): void {
  const key = kind === 'data' ? 'onData' : kind === 'closed' ? 'onClosed' : 'onError'
  const cb = fake.mocks.sessions[key].mock.calls[0]?.[0] as
    ((p: SessionEventPayload) => void) | undefined
  if (!cb) throw new Error(`emitSessionEvent: ${key} never subscribed`)
  cb(payload)
}

type AgentEventKind = 'delta' | 'tool' | 'done' | 'error'
type AgentEventPayload = AgentDeltaEvent | AgentToolEvent | AgentDoneEvent | AgentErrorEvent

/** Deliver an agent event to the callback the panel registered on mount. */
export function emitAgentEvent(
  fake: FakeApi,
  kind: AgentEventKind,
  payload: AgentEventPayload
): void {
  const key = ('on' + kind[0].toUpperCase() + kind.slice(1)) as
    | 'onDelta'
    | 'onTool'
    | 'onDone'
    | 'onError'
  const cb = fake.mocks.agent[key].mock.calls[0]?.[0] as
    ((p: AgentEventPayload) => void) | undefined
  if (!cb) throw new Error(`emitAgentEvent: ${key} never subscribed`)
  cb(payload)
}

/** Deliver a permission event to the callback App registered on mount. */
export function emitPermissionAsk(fake: FakeApi, event: PermissionAskEvent): void {
  const cb = fake.mocks.permission.onAsk.mock.calls[0]?.[0] as
    | ((p: PermissionAskEvent) => void)
    | undefined
  if (!cb) throw new Error('emitPermissionAsk: onAsk never subscribed')
  cb(event)
}

export function emitPermissionClosed(fake: FakeApi, event: PermissionClosedEvent): void {
  const cb = fake.mocks.permission.onClosed.mock.calls[0]?.[0] as
    | ((p: PermissionClosedEvent) => void)
    | undefined
  if (!cb) throw new Error('emitPermissionClosed: onClosed never subscribed')
  cb(event)
}

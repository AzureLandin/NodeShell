export type AuthMethod = 'password' | 'privateKey'

export type LanguageCode = 'zh' | 'en'

export type ThemePreference = 'system' | 'light' | 'dark'

export type ResolvedTheme = 'light' | 'dark'

export interface AppSettings {
  language: LanguageCode
  terminalFontFamily: string
  terminalFontSize: number
  /** MCP SSH session idle timeout in minutes (default 10). */
  mcpIdleTimeoutMinutes: number
  /** Max concurrent MCP SSH sessions (default 8). */
  mcpMaxSessions: number
  /** UI + terminal theme preference (default system). */
  themePreference: ThemePreference
  /**
   * Named OpenAI-compatible providers for the sidebar agent. API keys are not
   * part of the settings file; they live in the OS keyring and are written
   * through api.agent.setProviderKey.
   */
  agentProviders?: AgentProvider[]
  agentDefaultProviderId?: string
  agentDefaultModel?: string
  /**
   * Pre-multi-provider endpoint fields. Still read for migration; new writes
   * go through agentProviders.
   */
  agentBaseUrl?: string
  /** Model id the sidebar agent requests (legacy single-endpoint field). */
  agentModel?: string
  /**
   * How sensitive agent/MCP tools (commands, writes, uploads, downloads)
   * are authorised. Default `ask`.
   */
  permissionPolicy?: PermissionPolicy
}

export interface AgentProvider {
  id: string
  name: string
  baseUrl: string
  models: string[]
}

export interface HostConfig {
  id: string
  name: string
  host: string
  port: number
  username: string
  authMethod: AuthMethod
  /** Absolute path; only used when authMethod === 'privateKey' */
  privateKeyPath?: string
  credentialsPrompted?: boolean
  credentialsSaved?: boolean
}

export type HostInput = Omit<HostConfig, 'id'>

export type SshErrorCode =
  | 'CONNECTION_REFUSED'
  | 'TIMEOUT'
  | 'AUTH_FAILED'
  | 'HOST_UNREACHABLE'
  | 'HOST_KEY_CHANGED'
  | 'HOST_KEY_UNKNOWN'
  | 'CONFIG_READ_FAILED'
  | 'CONFIG_WRITE_FAILED'
  | 'SESSION_NOT_FOUND'
  | 'MCP_SESSION_LIMIT'
  | 'HOST_NOT_FOUND'
  | 'CANCELLED'
  | 'PERMISSION_DENIED'
  | 'UNKNOWN'

export interface AppError {
  code: SshErrorCode
  message: string
}

export interface ConnectOptions {
  password?: string
  /** When true, accept and store a new/changed host key */
  acceptHostKey?: boolean
}

export interface SessionDataEvent {
  sessionId: string
  data: string
}

export interface SessionClosedEvent {
  sessionId: string
}

export interface SessionErrorEvent {
  sessionId: string
  error: AppError
}

export interface MonitorProcess {
  memBytes: number
  cpuPercent: number
  command: string
}

export interface MonitorSnapshot {
  title: string
  cpuPercent: number | null
  memUsedBytes: number
  memTotalBytes: number
  swapUsedBytes: number
  swapTotalBytes: number
  load1: number
  load5: number
  load15: number
  netRxBps: number | null
  netTxBps: number | null
  processes: MonitorProcess[]
  updatedAt: number
}

export interface MonitorUpdateEvent {
  sessionId: string | null
  snapshot: MonitorSnapshot | null
  error?: string
}

export interface SftpTransferProgressEvent {
  sessionId: string
  direction: 'up' | 'down'
  name: string
  transferred: number
  total: number
  done: boolean
}

export type TransferDirection = 'upload' | 'download'
export type TransferState = 'queued' | 'running' | 'finalizing' | 'succeeded' | 'failed' | 'cancelled'

export interface TransferTask {
  taskId: string
  sessionId: string
  sessionTitle: string
  direction: TransferDirection
  name: string
  remotePath: string
  transferred: number
  total: number
  state: TransferState
  error?: string
  createdAt: number
  startedAt?: number
  finishedAt?: number
  retryOf?: string
}

/** One streamed assistant text fragment. */
export interface AgentDeltaEvent {
  sessionId: string
  delta: string
}

/** One finished agent tool call; `summary` is the command or remote path. */
export interface AgentToolEvent {
  sessionId: string
  callId: string
  name: string
  summary: string
  ok: boolean
  detail?: string
}

/** Closes one agent run; exactly one arrives per accepted prompt. */
export interface AgentDoneEvent {
  sessionId: string
  aborted: boolean
}

/** A failure after the prompt was accepted; always followed by done. */
export interface AgentErrorEvent {
  sessionId: string
  error: AppError
}

export type PermissionPolicy = 'ask' | 'allow' | 'deny'

export type PermissionDecision = 'deny' | 'allow' | 'allow-session'

export type PermissionSource = 'agent' | 'mcp'

/** One in-app permission prompt. Summary is a command or path, never file contents. */
export interface PermissionAskEvent {
  id: string
  source: PermissionSource
  tool: string
  sessionId: string
  title: string
  summary: string
  detail?: string
}

export interface PermissionClosedEvent {
  id: string
}

/** One remote TCP listener that can be forwarded locally. */
export interface TunnelListener {
  bind: string
  port: number
}

/** One live local port forward. */
export interface Tunnel {
  id: string
  sessionId: string
  localHost: string
  localPort: number
  remoteAddr: string
  remotePort: number
}

/** Agent endpoint state; API keys are never returned, only whether each provider has one. */
export interface AgentProviderStatus {
  id: string
  name: string
  baseUrl: string
  models: string[]
  hasKey: boolean
}

export interface AgentConfigStatus {
  configured: boolean
  providers: AgentProviderStatus[]
  defaultProviderId: string
  defaultModel: string
}

export interface AgentProviderInput {
  id?: string
  name: string
  baseUrl: string
  models: string[]
}

export interface ElectronApi {
  hosts: {
    list: () => Promise<HostConfig[]>
    create: (input: HostInput) => Promise<HostConfig>
    update: (id: string, patch: Partial<HostInput>) => Promise<HostConfig>
    remove: (id: string) => Promise<void>
  }
  sessions: {
    connect: (
      hostId: string,
      options?: ConnectOptions
    ) => Promise<{ sessionId: string }>
    write: (sessionId: string, data: string) => void
    resize: (sessionId: string, cols: number, rows: number) => Promise<void>
    disconnect: (sessionId: string) => Promise<void>
    cancelConnect: () => Promise<void>
    onData: (cb: (event: SessionDataEvent) => void) => () => void
    onClosed: (cb: (event: SessionClosedEvent) => void) => () => void
    onError: (cb: (event: SessionErrorEvent) => void) => () => void
  }
  settings: {
    get: () => Promise<AppSettings>
    set: (patch: Partial<AppSettings>) => Promise<AppSettings>
  }
  credentials: {
    isAvailable: () => Promise<boolean>
    save: (
      hostId: string,
      payload: { password?: string; privateKeyPath?: string }
    ) => Promise<void>
    clear: (hostId: string) => Promise<void>
    markPrompted: (hostId: string, saved: boolean) => Promise<void>
  }
  sftp: {
    list: (sessionId: string) => Promise<
      Array<{
        name: string
        path: string
        isDirectory: boolean
        size: number
        modifyTime: number
      }>
    >
    cwd: (sessionId: string) => Promise<string>
    chdir: (sessionId: string, remotePath: string) => Promise<string>
    mkdir: (sessionId: string, name: string) => Promise<void>
    rename: (sessionId: string, from: string, to: string) => Promise<void>
    remove: (sessionId: string, remotePath: string) => Promise<void>
    upload: (sessionId: string) => Promise<void>
    uploadPaths: (sessionId: string, localPaths: string[]) => Promise<void>
    download: (sessionId: string, remotePath: string, defaultName: string) => Promise<void>
    /** Read a remote text file for the in-app editor (max 512KiB). */
    readText: (sessionId: string, remotePath: string) => Promise<{ path: string; content: string }>
    /** Write UTF-8 text to a remote file from the in-app editor (max 512KiB). */
    writeText: (
      sessionId: string,
      remotePath: string,
      content: string
    ) => Promise<{ path: string }>
    onTransferProgress: (cb: (event: SftpTransferProgressEvent) => void) => () => void
  }
  transfer: {
    getTasks: () => Promise<TransferTask[]>
    enqueueUpload: (sessionId: string, remoteDir: string, localPaths: string[]) => Promise<string[]>
    enqueueDownload: (sessionId: string, remotePath: string, localPath: string) => Promise<string>
    chooseUploadFiles: (sessionId: string, remoteDir: string) => Promise<string[]>
    chooseDownloadTarget: (sessionId: string, remotePath: string, defaultName: string) => Promise<string>
    cancel: (taskId: string) => Promise<void>
    retry: (taskId: string) => Promise<string>
    clear: (taskId: string) => Promise<void>
    clearCompleted: () => Promise<void>
    onTask: (cb: (task: TransferTask) => void) => () => void
  }
  files: {
    /** Resolve OS path for a File from drag-drop (Electron webUtils). */
    getPathForFile: (file: File) => string
    /**
     * Subscribe to native file drops (Wails OnFileDrop). Present only in the
     * Wails adapter, where DOM File objects carry no real path; the Electron
     * preload keeps the DOM drop path and omits this. Returns an idempotent
     * unsubscribe.
     */
    onDrop?: (cb: (paths: string[]) => void) => () => void
  }
  monitor: {
    setActive: (sessionId: string | null, title?: string) => Promise<void>
    onUpdate: (cb: (event: MonitorUpdateEvent) => void) => () => void
  }
  tunnels: {
    discover: (sessionId: string) => Promise<TunnelListener[]>
    start: (sessionId: string, remoteAddr: string, remotePort: number) => Promise<Tunnel>
    stop: (sessionId: string, tunnelId: string) => Promise<void>
    list: (sessionId: string) => Promise<Tunnel[]>
  }
  /**
   * Sidebar assistant bound to one SSH session. Present only in the Wails
   * adapter (the Electron preload predates it), so callers must tolerate it
   * being absent.
   */
  agent?: {
    status: () => Promise<AgentConfigStatus>
    upsertProvider: (input: AgentProviderInput) => Promise<AgentConfigStatus>
    deleteProvider: (id: string) => Promise<AgentConfigStatus>
    setProviderKey: (id: string, apiKey: string) => Promise<AgentConfigStatus>
    setDefaultModel: (providerId: string, model: string) => Promise<AgentConfigStatus>
    /**
     * Accept one message for the session. Rejects only on a pre-flight
     * failure (not configured, empty prompt, a run already in flight);
     * progress arrives through the events below.
     */
    prompt: (
      sessionId: string,
      title: string,
      text: string,
      providerId: string,
      model: string
    ) => Promise<void>
    abort: (sessionId: string) => Promise<void>
    clear: (sessionId: string) => Promise<void>
    onDelta: (cb: (event: AgentDeltaEvent) => void) => () => void
    onTool: (cb: (event: AgentToolEvent) => void) => () => void
    onDone: (cb: (event: AgentDoneEvent) => void) => () => void
    onError: (cb: (event: AgentErrorEvent) => void) => () => void
  }
  permission: {
    decide: (id: string, decision: PermissionDecision) => Promise<void>
    onAsk: (cb: (event: PermissionAskEvent) => void) => () => void
    onClosed: (cb: (event: PermissionClosedEvent) => void) => () => void
  }
  fonts: {
    list: () => Promise<string[]>
  }
  app: {
    getVersion: () => Promise<string>
    /**
     * Open an http(s) URL in the system browser. Used by assistant markdown
     * links so a click cannot navigate the WebView.
     */
    openExternal?: (url: string) => Promise<void>
  }
  mcpRegistration: {
    status: () => Promise<McpRegistrationTargetStatus[]>
    register: (
      target: McpRegistrationTarget | 'all'
    ) => Promise<McpRegistrationResult[]>
    clipboardSnippet: () => Promise<string>
    manualConfig: () => Promise<McpManualConfig>
  }
  dialog: {
    openPrivateKeyFile: () => Promise<string | null>
  }
}

export type McpRegistrationTarget = 'cursor' | 'claudeCode' | 'codex' | 'opencode'

export type McpSnippetFormat = 'standard' | 'vscode' | 'opencode' | 'codex'

export interface McpManualConfig {
  command: string
  args: string[]
  snippets: {
    standard: string
    vscode: string
    opencode: string
    codex: string
  }
}

export interface McpRegistrationTargetStatus {
  id: McpRegistrationTarget
  label: string
  configPath: string
  registered: boolean
  stale: boolean
  detail?: string
}

export interface McpRegistrationResult {
  id: McpRegistrationTarget
  ok: boolean
  message: string
}

export const IPC = {
  hostsList: 'hosts:list',
  hostsCreate: 'hosts:create',
  hostsUpdate: 'hosts:update',
  hostsRemove: 'hosts:remove',
  sessionsConnect: 'sessions:connect',
  sessionsWrite: 'sessions:write',
  sessionsResize: 'sessions:resize',
  sessionsDisconnect: 'sessions:disconnect',
  sessionsCancelConnect: 'sessions:cancelConnect',
  sessionData: 'session:data',
  sessionClosed: 'session:closed',
  sessionError: 'session:error',
  settingsGet: 'settings:get',
  settingsSet: 'settings:set',
  credentialsIsAvailable: 'credentials:isAvailable',
  credentialsSave: 'credentials:save',
  credentialsClear: 'credentials:clear',
  credentialsMarkPrompted: 'credentials:markPrompted',
  sftpList: 'sftp:list',
  sftpCwd: 'sftp:cwd',
  sftpChdir: 'sftp:chdir',
  sftpMkdir: 'sftp:mkdir',
  sftpRename: 'sftp:rename',
  sftpRemove: 'sftp:remove',
  sftpUpload: 'sftp:upload',
  sftpUploadPaths: 'sftp:uploadPaths',
  sftpDownload: 'sftp:download',
  sftpReadText: 'sftp:readText',
  sftpWriteText: 'sftp:writeText',
  sftpTransferProgress: 'sftp:transferProgress',
  transferTask: 'transfer:task',
  transferGetTasks: 'transfer:getTasks',
  transferEnqueueUpload: 'transfer:enqueueUpload',
  transferEnqueueDownload: 'transfer:enqueueDownload',
  transferChooseUploadFiles: 'transfer:chooseUploadFiles',
  transferChooseDownloadTarget: 'transfer:chooseDownloadTarget',
  transferCancel: 'transfer:cancel',
  transferRetry: 'transfer:retry',
  transferClear: 'transfer:clear',
  transferClearCompleted: 'transfer:clearCompleted',
  monitorSetActive: 'monitor:setActive',
  monitorUpdate: 'monitor:update',
  tunnelsDiscover: 'tunnels:discover',
  tunnelsStart: 'tunnels:start',
  tunnelsStop: 'tunnels:stop',
  tunnelsList: 'tunnels:list',
  agentStatus: 'agent:status',
  agentPrompt: 'agent:prompt',
  agentAbort: 'agent:abort',
  agentClear: 'agent:clear',
  agentDelta: 'agent:delta',
  agentTool: 'agent:tool',
  agentDone: 'agent:done',
  agentError: 'agent:error',
  permissionAsk: 'permission:ask',
  permissionClosed: 'permission:closed',
  permissionDecide: 'permission:decide',
  fontsList: 'fonts:list',
  appGetVersion: 'app:getVersion',
  appOpenExternal: 'app:openExternal',
  mcpRegistrationStatus: 'mcpRegistration:status',
  mcpRegistrationRegister: 'mcpRegistration:register',
  mcpRegistrationClipboard: 'mcpRegistration:clipboardSnippet',
  mcpRegistrationManualConfig: 'mcpRegistration:manualConfig',
  dialogOpenPrivateKey: 'dialog:openPrivateKey',
  dialogOpenUploadFiles: 'dialog:openUploadFiles',
  dialogSaveDownload: 'dialog:saveDownload'
} as const

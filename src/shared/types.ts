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
  fonts: {
    list: () => Promise<string[]>
  }
  app: {
    getVersion: () => Promise<string>
  }
  mcpRegistration: {
    status: () => Promise<McpRegistrationTargetStatus[]>
    register: (
      target: McpRegistrationTarget | 'all'
    ) => Promise<McpRegistrationResult[]>
    clipboardSnippet: () => Promise<string>
  }
  dialog: {
    openPrivateKeyFile: () => Promise<string | null>
  }
}

export type McpRegistrationTarget = 'cursor' | 'claudeCode' | 'codex' | 'opencode'

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
  monitorSetActive: 'monitor:setActive',
  monitorUpdate: 'monitor:update',
  fontsList: 'fonts:list',
  appGetVersion: 'app:getVersion',
  mcpRegistrationStatus: 'mcpRegistration:status',
  mcpRegistrationRegister: 'mcpRegistration:register',
  mcpRegistrationClipboard: 'mcpRegistration:clipboardSnippet',
  dialogOpenPrivateKey: 'dialog:openPrivateKey',
  dialogOpenUploadFiles: 'dialog:openUploadFiles',
  dialogSaveDownload: 'dialog:saveDownload',
  filesOnDrop: 'files:onDrop'
} as const

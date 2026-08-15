import { useEffect, useRef, useState } from 'react'
import { useTranslation } from 'react-i18next'
import type { HostConfig, LanguageCode, PermissionAskEvent, PermissionDecision, PermissionPolicy, ThemePreference } from '../../shared/types'
import { FontAwesomeIcon } from '@fortawesome/react-fontawesome'
import { faChartLine, faGear } from '@fortawesome/free-solid-svg-icons'
import { AgentPanel } from './components/AgentPanel'
import { ConfirmModal } from './components/ConfirmModal'
import { ErrorBoundary } from './components/ErrorBoundary'
import type { HostFormSubmit } from './components/HostForm'
import { HostPickerModal } from './components/HostPickerModal'
import { ModalShell, useModalClose } from './components/ModalShell'
import { PasswordField } from './components/PasswordField'
import { PermissionModal } from './components/PermissionModal'
import { SessionTabs } from './components/SessionTabs'
import { SettingsModal } from './components/SettingsModal'
import { SidebarPanel } from './components/SidebarPanel'
import { Toast } from './components/Toast'
import { useHosts } from './hooks/useHosts'
import { ConnectError, type UiSession, useSessions } from './hooks/useSessions'
import { parseIpcThrownError } from '../../shared/ipc-error'
import i18n from './i18n'
import {
  applyResolvedTheme,
  getSystemPrefersDark,
  resolveTheme,
  subscribeSystemPrefersDark
} from './theme'

type PasswordAction =
  | { type: 'connect'; host: HostConfig }
  | { type: 'reconnect'; host: HostConfig; session: UiSession }

type ConfirmRequest = {
  title: string
  message: string
  confirmLabel?: string
  cancelLabel?: string
  resolve: (ok: boolean) => void
}

function PasswordModalBody({
  host,
  busy,
  error,
  onSubmit,
  onCancelConnect
}: {
  host: HostConfig
  busy: boolean
  error: string | null
  onSubmit: (password: string) => void
  onCancelConnect?: () => void
}): React.JSX.Element {
  const { t } = useTranslation()
  const requestClose = useModalClose()
  const [password, setPassword] = useState('')

  const handleSubmit = (e: React.FormEvent): void => {
    e.preventDefault()
    if (busy) return
    onSubmit(password)
  }

  return (
    <form onSubmit={handleSubmit} noValidate>
      <h3 id="password-modal-title" className="modal-title">
        {t('auth.passwordTitle', { name: host.name })}
      </h3>
      <p className="modal-subtitle">
        {host.username}@{host.host}:{host.port}
      </p>
      {busy && (
        <p className="session-connecting-status" role="status">
          {t('auth.connectingStatus', {
            name: host.name,
            host: host.host,
            port: host.port
          })}
        </p>
      )}
      <PasswordField
        label={t('auth.passwordLabel')}
        value={password}
        onChange={setPassword}
        autoFocus
        required
        disabled={busy}
        aria-invalid={Boolean(error)}
        aria-describedby={error ? 'password-modal-error' : undefined}
        className={error ? 'input-invalid' : undefined}
      />
      {error && (
        <p id="password-modal-error" className="form-inline-error" role="alert">
          {error}
        </p>
      )}
      <div className="form-actions">
        {busy && onCancelConnect ? (
          <button type="button" className="btn-secondary" onClick={onCancelConnect}>
            {t('auth.cancelConnect')}
          </button>
        ) : (
          <button type="button" className="btn-secondary" onClick={requestClose} disabled={busy}>
            {t('form.cancel')}
          </button>
        )}
        <button type="submit" className="btn-primary" disabled={busy}>
          {busy ? t('auth.connecting') : t('auth.connect')}
        </button>
      </div>
    </form>
  )
}

function PasswordModal({
  host,
  busy,
  error,
  onSubmit,
  onCancel,
  onCancelConnect
}: {
  host: HostConfig
  busy: boolean
  error: string | null
  onSubmit: (password: string) => void
  onCancel: () => void
  onCancelConnect?: () => void
}): React.JSX.Element {
  return (
    <ModalShell
      onClose={onCancel}
      dialogClassName="password-modal"
      labelledBy="password-modal-title"
      closeOnEscape={!busy}
      closeOnOverlayClick={false}
    >
      <PasswordModalBody
        host={host}
        busy={busy}
        error={error}
        onSubmit={onSubmit}
        onCancelConnect={onCancelConnect}
      />
    </ModalShell>
  )
}

function App(): React.JSX.Element {
  const { t } = useTranslation()
  const { hosts, error: hostsError, create, update, remove, refresh } = useHosts()
  const {
    sessions,
    activeSessionId,
    setActiveSessionId,
    toast: sessionsToast,
    setToast,
    connect,
    disconnect,
    abortConnectingUi,
    reconnect,
    registerDataListener
  } = useSessions()

  const [passwordAction, setPasswordAction] = useState<PasswordAction | null>(null)
  const [passwordError, setPasswordError] = useState<string | null>(null)
  const [connecting, setConnecting] = useState(false)
  const [connectingHost, setConnectingHost] = useState<HostConfig | null>(null)
  const passwordActionRef = useRef<PasswordAction | null>(null)
  const [language, setLanguage] = useState<LanguageCode>('zh')
  const [themePreference, setThemePreference] = useState<ThemePreference>('system')
  const [systemPrefersDark, setSystemPrefersDark] = useState(() =>
    typeof window !== 'undefined' ? getSystemPrefersDark() : true
  )
  const [terminalFontFamily, setTerminalFontFamily] = useState('Hack')
  const [terminalFontSize, setTerminalFontSize] = useState(14)
  const [mcpIdleTimeoutMinutes, setMcpIdleTimeoutMinutes] = useState(10)
  const [mcpMaxSessions, setMcpMaxSessions] = useState(8)
  const [permissionPolicy, setPermissionPolicy] = useState<PermissionPolicy>('ask')
  const [permissionQueue, setPermissionQueue] = useState<PermissionAskEvent[]>([])
  const [sftpExpanded, setSftpExpanded] = useState(false)
  const [monitorOpen, setMonitorOpen] = useState(true)
  const [agentOpen, setAgentOpen] = useState(true)
  const [settingsOpen, setSettingsOpen] = useState(false)
  const [hostsOpen, setHostsOpen] = useState(false)
  const [confirmRequest, setConfirmRequest] = useState<ConfirmRequest | null>(null)
  const connectingRef = useRef(false)
  const savePromptedRef = useRef(new Set<string>())
  const fontSizePersistTimerRef = useRef<number | null>(null)
  const fontSizePersistBaselineRef = useRef(14)

  passwordActionRef.current = passwordAction

  const resolvedTheme = resolveTheme(themePreference, systemPrefersDark)

  useEffect(() => {
    applyResolvedTheme(resolvedTheme)
  }, [resolvedTheme])

  useEffect(() => {
    if (themePreference !== 'system') return
    return subscribeSystemPrefersDark(setSystemPrefersDark)
  }, [themePreference])

  useEffect(() => {
    void (async () => {
      try {
        const settings = await window.api.settings.get()
        setLanguage(settings.language)
        setThemePreference(settings.themePreference)
        setTerminalFontFamily(settings.terminalFontFamily)
        setTerminalFontSize(settings.terminalFontSize)
        fontSizePersistBaselineRef.current = settings.terminalFontSize
        setMcpIdleTimeoutMinutes(settings.mcpIdleTimeoutMinutes)
        setMcpMaxSessions(settings.mcpMaxSessions)
        setPermissionPolicy(settings.permissionPolicy ?? 'ask')
        await i18n.changeLanguage(settings.language)
      } catch {
        setLanguage('zh')
        setThemePreference('system')
        setTerminalFontFamily('Hack')
        setTerminalFontSize(14)
        fontSizePersistBaselineRef.current = 14
        setMcpIdleTimeoutMinutes(10)
        setMcpMaxSessions(8)
        setPermissionPolicy('ask')
        await i18n.changeLanguage('zh')
      }
    })()
    return () => {
      if (fontSizePersistTimerRef.current != null) {
        window.clearTimeout(fontSizePersistTimerRef.current)
      }
    }
  }, [])

  useEffect(() => {
    const offAsk = window.api.permission.onAsk((event) => {
      setPermissionQueue((q) => (q.some((r) => r.id === event.id) ? q : [...q, event]))
    })
    const offClosed = window.api.permission.onClosed((event) => {
      setPermissionQueue((q) => q.filter((r) => r.id !== event.id))
    })
    return () => {
      offAsk()
      offClosed()
    }
  }, [])

  const askConfirm = (request: Omit<ConfirmRequest, 'resolve'>): Promise<boolean> =>
    new Promise((resolve) => {
      setConfirmRequest({ ...request, resolve })
    })

  const localizeConnectError = (
    e: unknown,
    fallbackKey: 'auth.connectionFailed' | 'auth.reconnectFailed',
    host?: HostConfig | null
  ): string => {
    const parsed = e instanceof ConnectError ? { code: e.code, message: e.message } : parseIpcThrownError(e)
    const code = parsed.code
    const parsedMessage = parsed.message
    const addr = {
      name: host?.name ?? '',
      host: host?.host ?? '',
      port: host?.port ?? ''
    }
    if (code === 'CANCELLED') return t('auth.cancelled')
    if (
      code === 'AUTH_FAILED' ||
      /authentication failed/i.test(parsedMessage) ||
      parsedMessage.includes('"AUTH_FAILED"')
    ) {
      return t('auth.authFailed')
    }
    if (code === 'CONNECTION_REFUSED') return t('auth.connectionRefused', addr)
    if (code === 'HOST_UNREACHABLE') return t('auth.hostUnreachable', addr)
    if (code === 'TIMEOUT') return t('auth.timeout', addr)
    return parsedMessage || t(fallbackKey)
  }

  const hostKeyFingerprintLabel = (message: string): string => {
    // Go's HOST_KEY_CHANGED error carries both keys
    // ("was SHA256:<old>, now SHA256:<new>"); the dialog must show the NEW
    // key being accepted, so take the last SHA256 match (HOST_KEY_UNKNOWN has
    // a single match, so last == first and is unaffected).
    const matches = message.match(/SHA256:[A-Za-z0-9+/=]+/g)
    const last = matches?.[matches.length - 1]
    return last ? `SHA256:\n${last.slice('SHA256:'.length)}` : message
  }

  const handleLanguageChange = async (next: LanguageCode): Promise<void> => {
    const previous = language
    setLanguage(next)
    await i18n.changeLanguage(next)
    try {
      await window.api.settings.set({ language: next })
    } catch {
      setLanguage(previous)
      await i18n.changeLanguage(previous)
      setToast(t('auth.connectionFailed'))
    }
  }

  const handleThemePreferenceChange = async (next: ThemePreference): Promise<void> => {
    const previous = themePreference
    setThemePreference(next)
    try {
      const saved = await window.api.settings.set({ themePreference: next })
      setThemePreference(saved.themePreference)
    } catch {
      setThemePreference(previous)
      setToast(t('auth.connectionFailed'))
    }
  }

  const handleTerminalFontFamilyChange = async (next: string): Promise<void> => {
    const previous = terminalFontFamily
    setTerminalFontFamily(next)
    try {
      const saved = await window.api.settings.set({ terminalFontFamily: next })
      setTerminalFontFamily(saved.terminalFontFamily)
    } catch {
      setTerminalFontFamily(previous)
      setToast(t('auth.connectionFailed'))
    }
  }

  const handleTerminalFontSizeChange = (next: number): void => {
    setTerminalFontSize(next)
    if (fontSizePersistTimerRef.current != null) {
      window.clearTimeout(fontSizePersistTimerRef.current)
    }
    fontSizePersistTimerRef.current = window.setTimeout(() => {
      fontSizePersistTimerRef.current = null
      const baseline = fontSizePersistBaselineRef.current
      void (async () => {
        try {
          const saved = await window.api.settings.set({ terminalFontSize: next })
          setTerminalFontSize(saved.terminalFontSize)
          fontSizePersistBaselineRef.current = saved.terminalFontSize
        } catch {
          setTerminalFontSize(baseline)
          fontSizePersistBaselineRef.current = baseline
          setToast(t('auth.connectionFailed'))
        }
      })()
    }, 300)
  }

  const handleMcpIdleTimeoutMinutesChange = async (next: number): Promise<void> => {
    const previous = mcpIdleTimeoutMinutes
    setMcpIdleTimeoutMinutes(next)
    try {
      const saved = await window.api.settings.set({ mcpIdleTimeoutMinutes: next })
      setMcpIdleTimeoutMinutes(saved.mcpIdleTimeoutMinutes)
    } catch {
      setMcpIdleTimeoutMinutes(previous)
      setToast(t('auth.connectionFailed'))
    }
  }

  const handleMcpMaxSessionsChange = async (next: number): Promise<void> => {
    const previous = mcpMaxSessions
    setMcpMaxSessions(next)
    try {
      const saved = await window.api.settings.set({ mcpMaxSessions: next })
      setMcpMaxSessions(saved.mcpMaxSessions)
    } catch {
      setMcpMaxSessions(previous)
      setToast(t('auth.connectionFailed'))
    }
  }

  const handlePermissionPolicyChange = async (next: PermissionPolicy): Promise<void> => {
    const previous = permissionPolicy
    setPermissionPolicy(next)
    try {
      const saved = await window.api.settings.set({ permissionPolicy: next })
      setPermissionPolicy(saved.permissionPolicy ?? next)
    } catch {
      setPermissionPolicy(previous)
      setToast(t('auth.connectionFailed'))
    }
  }

  const handlePermissionDecide = (decision: PermissionDecision): void => {
    const current = permissionQueue[0]
    if (!current) return
    setPermissionQueue((q) => q.filter((r) => r.id !== current.id))
    void window.api.permission.decide(current.id, decision)
  }

  const maybePromptSaveCredentials = async (
    host: HostConfig,
    password?: string
  ): Promise<void> => {
    const latest = (await window.api.hosts.list()).find((h) => h.id === host.id) ?? host
    if (latest.credentialsPrompted || savePromptedRef.current.has(latest.id)) return
    savePromptedRef.current.add(latest.id)

    const save = await askConfirm({
      title: t('auth.saveCredentialsTitle'),
      message: t('auth.saveCredentials', { name: latest.name }),
      confirmLabel: t('auth.saveCredentialsConfirm'),
      cancelLabel: t('auth.saveCredentialsSkip')
    })

    if (!save) {
      await window.api.credentials.markPrompted(latest.id, false)
      await refresh()
      return
    }
    try {
      const available = await window.api.credentials.isAvailable()
      if (!available) {
        setToast(t('auth.credentialsUnavailable'))
        await window.api.credentials.markPrompted(latest.id, false)
        await refresh()
        return
      }
      await window.api.credentials.save(latest.id, {
        ...(password ? { password } : {}),
        ...(latest.privateKeyPath ? { privateKeyPath: latest.privateKeyPath } : {})
      })
      await refresh()
    } catch (e) {
      setToast(e instanceof Error ? e.message : t('auth.credentialsUnavailable'))
    }
  }

  const reportConnectFailure = (_host: HostConfig, message: string): void => {
    if (passwordActionRef.current) {
      setPasswordError(message)
      return
    }
    setToast(message)
  }

  const runConnect = async (
    host: HostConfig,
    options?: { password?: string; acceptHostKey?: boolean }
  ): Promise<boolean> => {
    try {
      await connect(host, options)
      setPasswordError(null)
      setPasswordAction(null)
      setConnectingHost(null)
      await maybePromptSaveCredentials(host, options?.password)
      return true
    } catch (e) {
      if (e instanceof ConnectError && e.code === 'CANCELLED') {
        reportConnectFailure(host, t('auth.cancelled'))
        return false
      }
      if (
        e instanceof ConnectError &&
        (e.code === 'HOST_KEY_CHANGED' || e.code === 'HOST_KEY_UNKNOWN')
      ) {
        const isUnknown = e.code === 'HOST_KEY_UNKNOWN'
        const accept = await askConfirm({
          title: isUnknown ? t('auth.hostKeyUnknownTitle') : t('auth.hostKeyChangedTitle'),
          message: isUnknown
            ? t('auth.hostKeyUnknown', { fingerprint: hostKeyFingerprintLabel(e.message) })
            : t('auth.hostKeyChanged', { fingerprint: hostKeyFingerprintLabel(e.message) })
        })
        if (accept) {
          return runConnect(host, { ...options, acceptHostKey: true })
        }
        abortConnectingUi(host.id)
        reportConnectFailure(host, localizeConnectError(e, 'auth.connectionFailed', host))
        return false
      }
      if (
        e instanceof ConnectError &&
        e.code === 'AUTH_FAILED' &&
        host.authMethod === 'password'
      ) {
        setPasswordError(t('auth.authFailed'))
        setPasswordAction({ type: 'connect', host })
        return false
      }
      reportConnectFailure(host, localizeConnectError(e, 'auth.connectionFailed', host))
      return false
    }
  }

  const attemptConnect = async (
    host: HostConfig,
    options?: { password?: string; acceptHostKey?: boolean }
  ): Promise<void> => {
    if (connectingRef.current) return
    connectingRef.current = true
    setConnecting(true)
    setConnectingHost(host)
    // Jump back to the terminal immediately; connecting state lives on the tab.
    setHostsOpen(false)
    setPasswordAction(null)
    setPasswordError(null)
    try {
      await runConnect(host, options)
    } finally {
      connectingRef.current = false
      setConnecting(false)
    }
  }

  const runReconnect = async (
    session: UiSession,
    host: HostConfig,
    options?: { password?: string; acceptHostKey?: boolean }
  ): Promise<boolean> => {
    try {
      await reconnect(session, host, options)
      setPasswordError(null)
      setPasswordAction(null)
      return true
    } catch (e) {
      if (e instanceof ConnectError && e.code === 'CANCELLED') {
        reportConnectFailure(host, t('auth.cancelled'))
        return false
      }
      if (
        e instanceof ConnectError &&
        (e.code === 'HOST_KEY_CHANGED' || e.code === 'HOST_KEY_UNKNOWN')
      ) {
        const isUnknown = e.code === 'HOST_KEY_UNKNOWN'
        const accept = await askConfirm({
          title: isUnknown ? t('auth.hostKeyUnknownTitle') : t('auth.hostKeyChangedTitle'),
          message: isUnknown
            ? t('auth.hostKeyUnknownReconnect', {
                fingerprint: hostKeyFingerprintLabel(e.message)
              })
            : t('auth.hostKeyChangedReconnect', {
                fingerprint: hostKeyFingerprintLabel(e.message)
              })
        })
        if (accept) {
          return runReconnect(session, host, { ...options, acceptHostKey: true })
        }
        abortConnectingUi(host.id)
        reportConnectFailure(host, localizeConnectError(e, 'auth.reconnectFailed', host))
        return false
      }
      if (
        e instanceof ConnectError &&
        e.code === 'AUTH_FAILED' &&
        host.authMethod === 'password'
      ) {
        setPasswordError(t('auth.authFailed'))
        setPasswordAction({ type: 'reconnect', session, host })
        return false
      }
      reportConnectFailure(host, localizeConnectError(e, 'auth.reconnectFailed', host))
      return false
    }
  }

  const attemptReconnect = async (
    session: UiSession,
    host: HostConfig,
    options?: { password?: string; acceptHostKey?: boolean }
  ): Promise<void> => {
    if (connectingRef.current) return
    connectingRef.current = true
    setConnecting(true)
    setConnectingHost(host)
    setPasswordAction(null)
    setPasswordError(null)
    try {
      await runReconnect(session, host, options)
    } finally {
      connectingRef.current = false
      setConnecting(false)
    }
  }

  const handleCancelConnect = (): void => {
    void window.api.sessions.cancelConnect()
  }

  const handleCreateHost = async ({ input, password }: HostFormSubmit): Promise<void> => {
    const host = await create(input)
    void attemptConnect(host, password ? { password } : undefined)
  }

  const handleUpdateHost = async (
    id: string,
    { input, password }: HostFormSubmit
  ): Promise<void> => {
    await update(id, input)
    if (!password) return
    try {
      const available = await window.api.credentials.isAvailable()
      if (!available) {
        setToast(t('auth.credentialsUnavailable'))
        return
      }
      await window.api.credentials.save(id, {
        password,
        ...(input.privateKeyPath ? { privateKeyPath: input.privateKeyPath } : {})
      })
      await refresh()
    } catch (e) {
      setToast(e instanceof Error ? e.message : t('auth.credentialsUnavailable'))
    }
  }

  const handleConnect = (host: HostConfig): void => {
    if (connectingRef.current) return
    if (host.authMethod === 'password' && !host.credentialsSaved) {
      setPasswordError(null)
      setPasswordAction({ type: 'connect', host })
      return
    }
    void attemptConnect(host)
  }

  const handlePasswordSubmit = (password: string): void => {
    if (!passwordAction || connectingRef.current) return
    setPasswordError(null)
    if (passwordAction.type === 'connect') {
      void attemptConnect(passwordAction.host, { password })
    } else {
      void attemptReconnect(passwordAction.session, passwordAction.host, { password })
    }
  }

  const handleReconnect = (session: UiSession): void => {
    if (connectingRef.current) return
    const host = hosts.find((h) => h.id === session.hostId)
    if (!host) {
      setToast(t('auth.hostNotFound'))
      return
    }
    if (host.authMethod === 'password' && !host.credentialsSaved) {
      setPasswordError(null)
      setPasswordAction({ type: 'reconnect', host, session })
      return
    }
    void attemptReconnect(session, host)
  }

  const toastMessage = sessionsToast
    ? localizeConnectError(sessionsToast, 'auth.connectionFailed')
    : hostsError
      ? localizeConnectError(hostsError, 'auth.connectionFailed')
      : null

  const activeSession = sessions.find((s) => s.sessionId === activeSessionId)
  const activeSessionTitle = activeSession?.title ?? null
  const activeConnected = activeSession?.status === 'connected'

  return (
    <div className="app">
      <nav className="icon-rail glass" aria-label={t('sidebar.railLabel')}>
        <button
          type="button"
          className="icon-rail-btn"
          aria-pressed={monitorOpen}
          aria-label={monitorOpen ? t('monitor.hide') : t('monitor.show')}
          title={monitorOpen ? t('monitor.hide') : t('monitor.show')}
          onClick={() => setMonitorOpen((v) => !v)}
        >
          <FontAwesomeIcon icon={faChartLine} aria-hidden />
        </button>
        <button
          type="button"
          className="icon-rail-btn"
          aria-label={t('settings.open')}
          title={t('settings.open')}
          onClick={() => setSettingsOpen(true)}
        >
          <FontAwesomeIcon icon={faGear} aria-hidden />
        </button>
      </nav>
      <aside
        className={`sidebar glass${monitorOpen ? '' : ' is-collapsed'}`}
        aria-hidden={!monitorOpen}
        inert={!monitorOpen ? true : undefined}
      >
        <div className="sidebar-inner">
          <ErrorBoundary>
            <SidebarPanel
              activeSessionId={activeSessionId}
              activeSessionTitle={activeSessionTitle}
              connected={activeConnected}
            />
          </ErrorBoundary>
        </div>
      </aside>
      <main className="main main-with-sessions">
        <ErrorBoundary>
          <SessionTabs
            sessions={sessions}
            activeSessionId={activeSessionId}
            onSelect={setActiveSessionId}
            onClose={(id) => void disconnect(id)}
            onReconnect={handleReconnect}
            onCancelConnect={handleCancelConnect}
            registerDataListener={registerDataListener}
            sftpExpanded={sftpExpanded}
            onToggleSftp={() => setSftpExpanded((v) => !v)}
            agentOpen={agentOpen}
            onToggleAgent={() => setAgentOpen((v) => !v)}
            onOpenHosts={() => setHostsOpen(true)}
            terminalFontFamily={terminalFontFamily}
            terminalFontSize={terminalFontSize}
            resolvedTheme={resolvedTheme}
            onTerminalFontSizeChange={(size) => void handleTerminalFontSizeChange(size)}
          />
        </ErrorBoundary>
      </main>
      {/* Stay mounted when collapsed so the transcript and event listeners
          survive; width animates to 0 and inert keeps it out of the tab order. */}
      <aside
        className={`agent-dock glass${agentOpen ? '' : ' is-collapsed'}`}
        aria-hidden={!agentOpen}
        inert={!agentOpen ? true : undefined}
      >
        <div className="agent-dock-inner">
          <ErrorBoundary>
            <AgentPanel
              activeSessionId={activeSessionId}
              activeSessionTitle={activeSessionTitle}
              connected={activeConnected}
              onOpenSettings={() => setSettingsOpen(true)}
              onHide={() => setAgentOpen(false)}
            />
          </ErrorBoundary>
        </div>
      </aside>
      {hostsOpen && (
        <HostPickerModal
          hosts={hosts}
          connecting={connecting}
          connectingHost={connectingHost}
          onConnect={handleConnect}
          onCreate={(result) => handleCreateHost(result)}
          onUpdate={(id, result) => handleUpdateHost(id, result)}
          onRemove={remove}
          onClose={() => {
            setHostsOpen(false)
            if (!connecting) setConnectingHost(null)
          }}
        />
      )}
      {settingsOpen && (
        <SettingsModal
          language={language}
          themePreference={themePreference}
          terminalFontFamily={terminalFontFamily}
          terminalFontSize={terminalFontSize}
          mcpIdleTimeoutMinutes={mcpIdleTimeoutMinutes}
          mcpMaxSessions={mcpMaxSessions}
          permissionPolicy={permissionPolicy}
          onLanguageChange={(lang) => void handleLanguageChange(lang)}
          onThemePreferenceChange={(theme) => void handleThemePreferenceChange(theme)}
          onTerminalFontFamilyChange={(family) => void handleTerminalFontFamilyChange(family)}
          onTerminalFontSizeChange={(size) => void handleTerminalFontSizeChange(size)}
          onMcpIdleTimeoutMinutesChange={(minutes) => void handleMcpIdleTimeoutMinutesChange(minutes)}
          onMcpMaxSessionsChange={(max) => void handleMcpMaxSessionsChange(max)}
          onPermissionPolicyChange={(policy) => void handlePermissionPolicyChange(policy)}
          onClose={() => setSettingsOpen(false)}
        />
      )}
      {passwordAction && (
        <PasswordModal
          host={passwordAction.host}
          busy={connecting}
          error={passwordError}
          onSubmit={handlePasswordSubmit}
          onCancelConnect={handleCancelConnect}
          onCancel={() => {
            if (!connectingRef.current) {
              setPasswordError(null)
              setPasswordAction(null)
            }
          }}
        />
      )}
      {confirmRequest && (
        <ConfirmModal
          title={confirmRequest.title}
          message={confirmRequest.message}
          confirmLabel={confirmRequest.confirmLabel}
          cancelLabel={confirmRequest.cancelLabel}
          onConfirm={() => {
            const { resolve } = confirmRequest
            setConfirmRequest(null)
            resolve(true)
          }}
          onCancel={() => {
            const { resolve } = confirmRequest
            setConfirmRequest(null)
            resolve(false)
          }}
        />
      )}
      {permissionQueue[0] && (
        <PermissionModal request={permissionQueue[0]} onDecide={handlePermissionDecide} />
      )}
      <Toast message={toastMessage} onClose={() => setToast(null)} />
    </div>
  )
}

export default App

import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { FontAwesomeIcon } from '@fortawesome/react-fontawesome'
import { faChartLine, faGear } from '@fortawesome/free-solid-svg-icons'
import { AgentPanel } from './features/agent'
import { useHosts, useHostCommands } from './features/hosts'
import { useConnectionFlow, SessionTabs } from './features/sessions'
import { useAppSettings } from './features/settings'
import { usePermissionQueue } from './features/permissions'
import { ErrorBoundary } from './ui/ErrorBoundary'
import { NotificationProvider } from './ui/notifications'
import { AppQueryProvider } from './app/query-provider'
import { useConfirmationQueue } from './app/use-confirmation-queue'
import { AppOverlays } from './app/AppOverlays'
import { SidebarPanel } from './app/SidebarPanel'

function AppContent(): React.JSX.Element {
  const { t } = useTranslation()
  const [sftpExpanded, setSftpExpanded] = useState(false)
  const [monitorOpen, setMonitorOpen] = useState(true)
  const [agentOpen, setAgentOpen] = useState(false)
  const [settingsOpen, setSettingsOpen] = useState(false)
  const [hostsOpen, setHostsOpen] = useState(false)
  const hosts = useHosts()
  const settings = useAppSettings()
  const confirmation = useConfirmationQueue()
  const flow = useConnectionFlow({
    hosts: hosts.hosts,
    refreshHosts: hosts.refresh,
    askConfirm: confirmation.ask,
    onConnectStart: () => setHostsOpen(false)
  })
  const hostCommands = useHostCommands(hosts, flow.connect, flow.reportCredentialError)
  const permissions = usePermissionQueue(() => setAgentOpen(true))
  const { sessions, activeSessionId, setActiveSessionId, disconnect, registerDataListener } =
    flow.session
  const activeSession = sessions.find((item) => item.sessionId === activeSessionId)
  const activeSessionTitle = activeSession?.title ?? null
  const activeConnected = activeSession?.status === 'connected'
  const decidePermission = (decision: Parameters<typeof permissions.decide>[1]): void => {
    if (permissions.current) permissions.decide(permissions.current.id, decision)
  }

  return (
    <div className="app">
      <nav className="icon-rail glass" aria-label={t('sidebar.railLabel')}>
        <button
          type="button"
          className="icon-rail-btn"
          aria-pressed={monitorOpen}
          aria-label={monitorOpen ? t('monitor.hide') : t('monitor.show')}
          title={monitorOpen ? t('monitor.hide') : t('monitor.show')}
          onClick={() => setMonitorOpen((value) => !value)}
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
            onReconnect={flow.reconnect}
            onCancelConnect={flow.cancelConnect}
            registerDataListener={registerDataListener}
            sftpExpanded={sftpExpanded}
            onToggleSftp={() => setSftpExpanded((value) => !value)}
            agentOpen={agentOpen}
            onToggleAgent={() => setAgentOpen((value) => !value)}
            onOpenHosts={() => {
              setHostsOpen(true)
              void hosts.refresh()
            }}
            terminalFontFamily={settings.values.terminalFontFamily}
            terminalFontSize={settings.values.terminalFontSize}
            resolvedTheme={settings.resolvedTheme}
            onTerminalFontSizeChange={(size) => settings.change('terminalFontSize', size)}
          />
        </ErrorBoundary>
      </main>
      {/* Keep the transcript and event subscriptions mounted while collapsed. */}
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
              permissionRequest={
                permissions.current?.source === 'agent' ? permissions.current : null
              }
              onPermissionDecide={decidePermission}
            />
          </ErrorBoundary>
        </div>
      </aside>
      <AppOverlays
        hosts={
          hostsOpen
            ? {
                hosts: hosts.hosts,
                loading: hosts.loading,
                refreshing: hosts.refreshing,
                loadError: hosts.error,
                hasData: hosts.hasData,
                onRefresh: () => {
                  void hosts.refresh()
                },
                connecting: flow.connecting,
                connectingHost: flow.connectingHost,
                onConnect: flow.connect,
                onCreate: hostCommands.create,
                onUpdate: hostCommands.update,
                onRemove: hosts.remove,
                onClose: () => setHostsOpen(false)
              }
            : null
        }
        settings={
          settingsOpen
            ? {
                ...settings.values,
                onLanguageChange: (value) => settings.change('language', value),
                onThemePreferenceChange: (value) => settings.change('themePreference', value),
                onTerminalFontFamilyChange: (value) => settings.change('terminalFontFamily', value),
                onTerminalFontSizeChange: (value) => settings.change('terminalFontSize', value),
                onMcpIdleTimeoutMinutesChange: (value) =>
                  settings.change('mcpIdleTimeoutMinutes', value),
                onMcpMaxSessionsChange: (value) => settings.change('mcpMaxSessions', value),
                onMcpPermissionModeChange: (value) => settings.change('mcpPermissionMode', value),
                onPermissionPolicyChange: (value) => settings.change('permissionPolicy', value),
                onClose: () => setSettingsOpen(false)
              }
            : null
        }
        password={
          flow.state.phase === 'password'
            ? {
                host: flow.state.action.host,
                error: flow.state.error,
                busy: flow.connecting,
                onSubmit: flow.submitPassword,
                onCancel: flow.dismissPassword,
                onCancelConnect: flow.cancelConnect
              }
            : null
        }
        confirmation={confirmation}
        permission={
          permissions.current && permissions.current.source !== 'agent'
            ? {
                key: permissions.current.id,
                request: permissions.current,
                onDecide: decidePermission
              }
            : null
        }
      />
    </div>
  )
}

export default function App(): React.JSX.Element {
  return (
    <AppQueryProvider>
      <NotificationProvider>
        <AppContent />
      </NotificationProvider>
    </AppQueryProvider>
  )
}

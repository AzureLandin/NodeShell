import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { FontAwesomeIcon } from '@fortawesome/react-fontawesome'
import { faChartLine, faNetworkWired } from '@fortawesome/free-solid-svg-icons'
import { TabsRoot, TabsList, TabsPanel, TabsTab } from '../ui/tabs'
import { TunnelPanel } from '../features/tunnels'
import { MonitorPanel } from '../features/monitoring'

interface SidebarPanelProps {
  activeSessionId: string | null
  activeSessionTitle: string | null
  connected: boolean
}

/**
 * Left sidebar: host monitor and on-demand SSH port forwards. Settings live
 * on the icon rail; the agent lives in the right-hand dock.
 */
export function SidebarPanel({
  activeSessionId,
  activeSessionTitle,
  connected
}: SidebarPanelProps): React.JSX.Element {
  const { t } = useTranslation()
  const [tab, setTab] = useState<'monitor' | 'ports'>('monitor')

  return (
    <TabsRoot
      className="sidebar-panel"
      value={tab}
      onValueChange={(value) => {
        if (value === 'monitor' || value === 'ports') setTab(value)
      }}
    >
      <TabsList
        className="sidebar-tabs"
        aria-label={t('sidebar.railLabel')}
        activateOnFocus={false}
      >
        <TabsTab value="monitor" className={tab === 'monitor' ? 'is-active' : undefined}>
          <FontAwesomeIcon icon={faChartLine} className="sidebar-tab-icon" aria-hidden="true" />
          <span>{t('monitor.title')}</span>
        </TabsTab>
        <TabsTab value="ports" className={tab === 'ports' ? 'is-active' : undefined}>
          <FontAwesomeIcon icon={faNetworkWired} className="sidebar-tab-icon" aria-hidden="true" />
          <span>{t('tunnels.title')}</span>
        </TabsTab>
      </TabsList>
      <TabsPanel className="sidebar-tab-panel" value="monitor" keepMounted>
        <MonitorPanel
          activeSessionId={activeSessionId}
          activeSessionTitle={activeSessionTitle}
          connected={connected}
        />
      </TabsPanel>
      <TabsPanel className="sidebar-tab-panel" value="ports" keepMounted>
        <TunnelPanel activeSessionId={activeSessionId} connected={connected} />
      </TabsPanel>
    </TabsRoot>
  )
}

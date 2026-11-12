import { Tabs } from '@base-ui/react/tabs'
import type { ReactNode } from 'react'

export interface TabsRootProps extends Omit<Tabs.Root.Props, 'children'> {
  children: ReactNode
}

/** Controlled tabs use value/onValueChange; orientation defaults to horizontal. */
export function TabsRoot({ children, ...props }: TabsRootProps): React.JSX.Element {
  return <Tabs.Root {...props}>{children}</Tabs.Root>
}

export interface TabsListProps extends Tabs.List.Props {}

/** Roving keyboard navigation; activateOnFocus defaults to false (Enter/Space selects). */
export function TabsList(props: TabsListProps): React.JSX.Element {
  return <Tabs.List {...props} />
}

export interface TabsTabProps extends Tabs.Tab.Props {}

/** A single tab button; renders `role="tab"`. */
export function TabsTab(props: TabsTabProps): React.JSX.Element {
  return <Tabs.Tab {...props} />
}

export interface TabsPanelProps extends Tabs.Panel.Props {}

/** Use keepMounted for long-lived content; inactive panels stay hidden and inert. */
export function TabsPanel(props: TabsPanelProps): React.JSX.Element {
  return <Tabs.Panel {...props} />
}

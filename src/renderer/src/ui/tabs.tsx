import { Tabs } from '@base-ui/react/tabs'
import type { ReactNode } from 'react'

export interface FormTabsProps extends Omit<Tabs.Root.Props, 'children'> {
  /** Tab list and panels. */
  children: ReactNode
}

/** Tabs root built on Base UI `Tabs`. */
export function FormTabs({ children, ...props }: FormTabsProps): React.JSX.Element {
  return <Tabs.Root {...props}>{children}</Tabs.Root>
}

export interface FormTabsListProps extends Tabs.List.Props {}

/** Container for the tab buttons; renders `role="tablist"`. */
export function FormTabsList(props: FormTabsListProps): React.JSX.Element {
  return <Tabs.List {...props} />
}

export interface FormTabsTabProps extends Tabs.Tab.Props {}

/** A single tab button; renders `role="tab"`. */
export function FormTabsTab(props: FormTabsTabProps): React.JSX.Element {
  return <Tabs.Tab {...props} />
}

export interface FormTabsPanelProps extends Tabs.Panel.Props {}

/** Panel shown while the matching tab is active. */
export function FormTabsPanel(props: FormTabsPanelProps): React.JSX.Element {
  return <Tabs.Panel {...props} />
}

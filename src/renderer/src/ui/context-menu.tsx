import { useMemo, useRef, type ReactNode } from 'react'
import { Menu } from '@base-ui/react/menu'
import './ui.css'

/**
 * Controlled pointer-anchored menu built from Base UI's public Menu parts.
 *
 * The app opens the menu from a right-click on a terminal or a file row, so the
 * pointer position is already known when the menu opens. The wrapper turns
 * that position into the virtual anchor Base UI positions the popup against,
 * which keeps the "menu appears at the cursor" behaviour while Base UI owns
 * the collision handling, the keyboard navigation and the dismissal. Menu
 * supports this external opening model without ContextMenu.Trigger's gesture
 * timing or modal backdrop, so another row can receive the next right-click.
 */

export interface ContextMenuSurfaceProps {
  /** Viewport X coordinate of the pointer that opened the menu. */
  x: number
  /** Viewport Y coordinate of the pointer that opened the menu. */
  y: number
  /** Whether the menu is open. */
  open: boolean
  /** Called when Base UI asks to open or close the menu. */
  onOpenChange: NonNullable<Menu.Root.Props['onOpenChange']>
  /** Domain focus target; outside presses and newly focused dialogs take priority. */
  finalFocus?: Menu.Popup.Props['finalFocus']
  /** Class name of the popup element (the element carrying `role="menu"`). */
  className: string
  /** Test id forwarded to the popup. */
  testId?: string
  children: ReactNode
}

/** Root + portal + positioner + popup of a context menu. */
export function ContextMenuSurface({
  x,
  y,
  open,
  onOpenChange,
  finalFocus = true,
  className,
  testId,
  children
}: ContextMenuSurfaceProps): React.JSX.Element {
  const closeReason = useRef<Menu.Root.ChangeEventDetails['reason'] | null>(null)
  const popupRef = useRef<HTMLDivElement | null>(null)
  const anchor = useMemo(
    () => ({
      getBoundingClientRect: () => DOMRect.fromRect({ x, y, width: 0, height: 0 })
    }),
    [x, y]
  )

  return (
    <Menu.Root
      open={open}
      modal={false}
      onOpenChange={(next, details) => {
        closeReason.current = next ? null : details.reason
        onOpenChange(next, details)
      }}
    >
      <Menu.Portal>
        <Menu.Positioner
          anchor={anchor}
          side="bottom"
          align="start"
          sideOffset={0}
          alignOffset={0}
          data-bui-context-menu-positioner=""
        >
          <Menu.Popup
            ref={(element) => {
              // Keep the last popup for the focus manager's unmount cleanup.
              if (element) popupRef.current = element
            }}
            finalFocus={(interaction) => {
              if (closeReason.current === 'outside-press' || closeReason.current === 'focus-out') {
                return false
              }
              const popup = popupRef.current
              const focused = popup?.ownerDocument.activeElement
              if (focused && focused !== popup?.ownerDocument.body && !popup?.contains(focused)) {
                return false
              }
              if (typeof finalFocus === 'function') return finalFocus(interaction)
              return typeof finalFocus === 'object' ? finalFocus.current : finalFocus
            }}
            className={className}
            data-testid={testId}
            data-bui-context-menu-popup=""
          >
            {children}
          </Menu.Popup>
        </Menu.Positioner>
      </Menu.Portal>
    </Menu.Root>
  )
}

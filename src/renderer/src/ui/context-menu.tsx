import { useMemo, type ReactNode } from 'react'
import { ContextMenu } from '@base-ui/react/context-menu'
import './ui.css'

/**
 * Thin wrapper around Base UI's context menu.
 *
 * The app opens the menu from a right-click on a terminal or a file row, so the
 * pointer position is already known when the menu mounts. The wrapper turns
 * that position into the virtual anchor Base UI positions the popup against,
 * which keeps the "menu appears at the cursor" behaviour while Base UI owns
 * the collision handling, the keyboard navigation and the dismissal.
 */

export interface ContextMenuSurfaceProps {
  /** Viewport X coordinate of the pointer that opened the menu. */
  x: number
  /** Viewport Y coordinate of the pointer that opened the menu. */
  y: number
  /** Whether the menu is open. */
  open: boolean
  /** Called when Base UI asks to open or close the menu. */
  onOpenChange: (open: boolean) => void
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
  className,
  testId,
  children
}: ContextMenuSurfaceProps): React.JSX.Element {
  const anchor = useMemo(
    () => ({
      getBoundingClientRect: () => DOMRect.fromRect({ x, y, width: 0, height: 0 })
    }),
    [x, y]
  )

  return (
    <ContextMenu.Root open={open} onOpenChange={(next) => onOpenChange(next)}>
      <ContextMenu.Portal>
        <ContextMenu.Positioner
          anchor={anchor}
          side="bottom"
          align="start"
          sideOffset={0}
          alignOffset={0}
          data-bui-context-menu-positioner=""
        >
          <ContextMenu.Popup
            className={className}
            data-testid={testId}
            data-bui-context-menu-popup=""
          >
            {children}
          </ContextMenu.Popup>
        </ContextMenu.Positioner>
      </ContextMenu.Portal>
    </ContextMenu.Root>
  )
}

import type { ReactNode } from 'react'
import { Menu, type MenuPositionerProps } from '@base-ui/react/menu'
import './ui.css'

/**
 * Thin wrappers around Base UI's menu primitives.
 *
 * The wrappers only carry the class names the app already uses, so `App.css`
 * keeps styling the surfaces untouched, plus a `data-bui-*` hook that the
 * scoped rules in `ui.css` target. Positioning, keyboard navigation,
 * dismissal, focus handling and close-on-select are Base UI's job.
 */

/** Anchor accepted by Base UI's positioner (element, ref or virtual element). */
export type MenuAnchor = MenuPositionerProps['anchor']

export interface MenuRootProps {
  /** Whether the menu is open. */
  open: boolean
  /** Called when Base UI asks to open or close the menu. */
  onOpenChange: (open: boolean) => void
  children: ReactNode
}

/** Groups the parts of a menu. Renders no element of its own. */
export function MenuRoot({ open, onOpenChange, children }: MenuRootProps): React.JSX.Element {
  return (
    <Menu.Root open={open} onOpenChange={(next) => onOpenChange(next)}>
      {children}
    </Menu.Root>
  )
}

export interface MenuTriggerProps {
  /** Class name for the trigger button. */
  className: string
  children: ReactNode
}

/** Button that opens the menu it belongs to. */
export function MenuTrigger({ className, children }: MenuTriggerProps): React.JSX.Element {
  return (
    <Menu.Trigger className={className} data-bui-menu-trigger="">
      {children}
    </Menu.Trigger>
  )
}

export interface MenuSurfaceProps {
  /** Class name of the popup element (the element carrying `role="menu"`). */
  className: string
  /** Class name of the element Base UI positions. */
  positionerClassName?: string
  /** Anchor to position against. Defaults to the menu trigger. */
  anchor?: MenuAnchor
  /** Preferred side of the anchor. */
  side?: MenuPositionerProps['side']
  /** Alignment relative to the anchor. */
  align?: MenuPositionerProps['align']
  /** Distance between the anchor and the popup, in pixels. */
  sideOffset?: number
  /** Extra offset along the alignment axis, in pixels. */
  alignOffset?: number
  /** Minimum distance kept between the popup and the collision boundary. */
  collisionPadding?: MenuPositionerProps['collisionPadding']
  /** Test id forwarded to the popup. */
  testId?: string
  children: ReactNode
}

/**
 * Portal + positioner + popup of a menu.
 *
 * The popup is the element that carries `role="menu"`, so the class names used
 * by the app keep pointing at the same surface as before.
 */
export function MenuSurface({
  className,
  positionerClassName,
  anchor,
  side,
  align,
  sideOffset,
  alignOffset,
  collisionPadding,
  testId,
  children
}: MenuSurfaceProps): React.JSX.Element {
  return (
    <Menu.Portal>
      <Menu.Positioner
        className={positionerClassName}
        anchor={anchor}
        side={side}
        align={align}
        sideOffset={sideOffset}
        alignOffset={alignOffset}
        collisionPadding={collisionPadding}
        data-bui-menu-positioner=""
      >
        <Menu.Popup className={className} data-testid={testId} data-bui-menu-popup="">
          {children}
        </Menu.Popup>
      </Menu.Positioner>
    </Menu.Portal>
  )
}

export interface MenuItemProps {
  /** Class name for the item. */
  className: string
  /** Disables the item. Base UI marks it with `aria-disabled`/`data-disabled`. */
  disabled?: boolean
  /** Invoked when the item is activated. Never called while disabled. */
  onClick?: () => void
  children: ReactNode
}

/**
 * A single menu entry.
 *
 * It keeps the native `<button>` element the app used before, so the existing
 * `App.css` rules keep applying. Base UI takes care of the roving focus, the
 * highlight state and of not firing `onClick` while the item is disabled.
 */
export function MenuItem({
  className,
  disabled = false,
  onClick,
  children
}: MenuItemProps): React.JSX.Element {
  return (
    <Menu.Item
      className={className}
      disabled={disabled}
      onClick={onClick}
      nativeButton
      render={<button type="button" />}
      data-bui-menu-item=""
    >
      {children}
    </Menu.Item>
  )
}

export interface MenuSeparatorProps {
  /** Class name for the separator. */
  className: string
}

/** Divider between groups of menu items. */
export function MenuSeparator({ className }: MenuSeparatorProps): React.JSX.Element {
  return <Menu.Separator className={className} data-bui-menu-separator="" />
}

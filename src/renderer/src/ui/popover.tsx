import type { ReactNode } from 'react'
import { Popover } from '@base-ui/react/popover'
import type {
  PopoverCloseProps as BasePopoverCloseProps,
  PopoverPopupProps,
  PopoverPositionerProps,
  PopoverTriggerProps as BasePopoverTriggerProps
} from '@base-ui/react/popover'
import './ui.css'

/**
 * Thin wrappers around Base UI's popover primitives.
 *
 * The popover keeps the class names the app already uses so `App.css` keeps
 * styling the panel, while Base UI owns the positioning, the outside-press
 * dismissal, the Escape handling and the focus restoration.
 */

export interface PopoverRootProps {
  /** Whether the popover is open. */
  open: boolean
  /** Called when Base UI asks to open or close the popover. */
  onOpenChange: (open: boolean) => void
  children: ReactNode
}

/** Groups the parts of a popover. Renders no element of its own. */
export function PopoverRoot({ open, onOpenChange, children }: PopoverRootProps): React.JSX.Element {
  return (
    <Popover.Root open={open} onOpenChange={(next) => onOpenChange(next)}>
      {children}
    </Popover.Root>
  )
}

export type PopoverTriggerProps = Omit<BasePopoverTriggerProps, 'className' | 'children'> & {
  /** Class name for the trigger button. */
  className: string
  children: ReactNode
}

/** Button that toggles the popover it belongs to. */
export function PopoverTrigger({
  className,
  children,
  ...otherProps
}: PopoverTriggerProps): React.JSX.Element {
  return (
    <Popover.Trigger className={className} data-bui-popover-trigger="" {...otherProps}>
      {children}
    </Popover.Trigger>
  )
}

export interface PopoverSurfaceProps {
  /** Class name of the popup element. */
  className: string
  /** Class name of the element Base UI positions. */
  positionerClassName?: string
  /** Preferred side of the anchor. */
  side?: PopoverPositionerProps['side']
  /** Alignment relative to the anchor. */
  align?: PopoverPositionerProps['align']
  /** Distance between the anchor and the popup, in pixels. */
  sideOffset?: number
  /** Extra offset along the alignment axis, in pixels. */
  alignOffset?: number
  /** CSS positioning strategy used for the popup. */
  positionMethod?: PopoverPositionerProps['positionMethod']
  /** Minimum distance kept between the popup and the collision boundary. */
  collisionPadding?: PopoverPositionerProps['collisionPadding']
  /** Extra props forwarded to the popup element. */
  popupProps?: PopoverPopupProps
  children: ReactNode
}

/** Portal + positioner + popup of a popover. */
export function PopoverSurface({
  className,
  positionerClassName,
  side,
  align,
  sideOffset,
  alignOffset,
  positionMethod,
  collisionPadding,
  popupProps,
  children
}: PopoverSurfaceProps): React.JSX.Element {
  return (
    <Popover.Portal>
      <Popover.Positioner
        className={positionerClassName}
        side={side}
        align={align}
        sideOffset={sideOffset}
        alignOffset={alignOffset}
        positionMethod={positionMethod}
        collisionPadding={collisionPadding}
        data-bui-popover-positioner=""
      >
        <Popover.Popup className={className} data-bui-popover-popup="" {...popupProps}>
          {children}
        </Popover.Popup>
      </Popover.Positioner>
    </Popover.Portal>
  )
}

export type PopoverCloseProps = Omit<BasePopoverCloseProps, 'className' | 'children'> & {
  /** Class name for the close button. */
  className: string
  children: ReactNode
}

/** Button that closes the popover it belongs to. */
export function PopoverClose({
  className,
  children,
  ...otherProps
}: PopoverCloseProps): React.JSX.Element {
  return (
    <Popover.Close className={className} data-bui-popover-close="" {...otherProps}>
      {children}
    </Popover.Close>
  )
}

import { Select as BaseSelect } from '@base-ui/react/select'
import type { ReactNode } from 'react'
import './forms.css'

export interface SelectOption {
  value: string
  label: string
}

export interface SelectGroup {
  label: string
  options: SelectOption[]
}

/** Flat entry used to resolve the trigger label of the selected value. */
type SelectItemEntry = { label: string; value: string }

/** Grouped entry used to resolve the trigger label of the selected value. */
type SelectGroupEntry = { group: string; items: ReadonlyArray<SelectItemEntry> }

type SelectItems = ReadonlyArray<SelectItemEntry> | ReadonlyArray<SelectGroupEntry>

export interface SelectProps {
  value: string
  options?: SelectOption[]
  groups?: SelectGroup[]
  onChange: (value: string) => void
  disabled?: boolean
  placeholder?: string
  'aria-label'?: string
  id?: string
  className?: string
}

/**
 * Build the `items` description Base UI uses to render the label of the
 * selected value inside the trigger. Groups keep their shape so the label of
 * a grouped option still resolves.
 */
function buildItems(options: SelectOption[], groups?: SelectGroup[]): SelectItems {
  if (groups && groups.length > 0) {
    return groups.map((group) => ({
      group: group.label,
      items: group.options.map((option) => ({ label: option.label, value: option.value }))
    }))
  }
  return options.map((option) => ({ label: option.label, value: option.value }))
}

function renderOptions(options: SelectOption[]): ReactNode {
  return options.map((opt) => (
    <BaseSelect.Item key={opt.value} className="app-select-option" value={opt.value}>
      <BaseSelect.ItemText>{opt.label}</BaseSelect.ItemText>
    </BaseSelect.Item>
  ))
}

/**
 * Dropdown built on Base UI `Select`.
 *
 * The markup keeps the class names the previous Radix implementation used
 * (`app-select-trigger`, `app-select-content`, `app-select-option`, …) so the
 * existing design still applies; only the Base UI state attributes are added
 * on top of them.
 */
export function Select({
  value,
  options = [],
  groups,
  onChange,
  disabled = false,
  placeholder,
  id,
  className,
  'aria-label': ariaLabel
}: SelectProps): React.JSX.Element {
  const triggerClass = className ? `app-select-trigger ${className}` : 'app-select-trigger'
  const items = buildItems(options, groups)

  return (
    <BaseSelect.Root
      items={items}
      value={value}
      onValueChange={(next) => {
        if (next !== null) onChange(next)
      }}
      disabled={disabled}
    >
      <BaseSelect.Trigger id={id} className={triggerClass} aria-label={ariaLabel}>
        <BaseSelect.Value placeholder={placeholder} />
        <BaseSelect.Icon className="app-select-chevron" />
      </BaseSelect.Trigger>

      <BaseSelect.Portal>
        <BaseSelect.Positioner
          className="app-select-positioner"
          sideOffset={4}
          alignItemWithTrigger={false}
        >
          <BaseSelect.Popup className="app-select-content" data-base-ui-select-content>
            <BaseSelect.ScrollUpArrow className="app-select-scroll-btn">▴</BaseSelect.ScrollUpArrow>
            <BaseSelect.List className="app-select-viewport" data-base-ui-select-list>
              {groups && groups.length > 0
                ? groups.map((group, i) => (
                    <BaseSelect.Group key={`${group.label}-${i}`}>
                      <BaseSelect.GroupLabel className="app-select-group-label">
                        {group.label}
                      </BaseSelect.GroupLabel>
                      {renderOptions(group.options)}
                    </BaseSelect.Group>
                  ))
                : renderOptions(options)}
            </BaseSelect.List>
            <BaseSelect.ScrollDownArrow className="app-select-scroll-btn">
              ▾
            </BaseSelect.ScrollDownArrow>
          </BaseSelect.Popup>
        </BaseSelect.Positioner>
      </BaseSelect.Portal>
    </BaseSelect.Root>
  )
}

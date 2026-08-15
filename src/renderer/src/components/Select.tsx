import type { ReactNode } from 'react'
import * as RadixSelect from '@radix-ui/react-select'

export interface SelectOption {
  value: string
  label: string
}

export interface SelectGroup {
  label: string
  options: SelectOption[]
}

interface SelectProps {
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

function renderOptions(options: SelectOption[]): ReactNode {
  return options.map((opt) => (
    <RadixSelect.Item key={opt.value} className="app-select-option" value={opt.value}>
      <RadixSelect.ItemText>{opt.label}</RadixSelect.ItemText>
    </RadixSelect.Item>
  ))
}

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
  return (
    <RadixSelect.Root value={value} onValueChange={onChange} disabled={disabled}>
      <RadixSelect.Trigger id={id} className={triggerClass} aria-label={ariaLabel}>
        <RadixSelect.Value placeholder={placeholder} />
        <RadixSelect.Icon asChild>
          <span className="app-select-chevron" aria-hidden />
        </RadixSelect.Icon>
      </RadixSelect.Trigger>

      <RadixSelect.Portal>
        <RadixSelect.Content
          className="app-select-content"
          position="popper"
          sideOffset={4}
          onEscapeKeyDown={(e) => e.stopPropagation()}
        >
          <RadixSelect.ScrollUpButton className="app-select-scroll-btn">▴</RadixSelect.ScrollUpButton>
          <RadixSelect.Viewport className="app-select-viewport">
            {groups && groups.length > 0
              ? groups.map((group, i) => (
                  <RadixSelect.Group key={`${group.label}-${i}`}>
                    <RadixSelect.Label className="app-select-group-label">{group.label}</RadixSelect.Label>
                    {renderOptions(group.options)}
                  </RadixSelect.Group>
                ))
              : renderOptions(options)}
          </RadixSelect.Viewport>
          <RadixSelect.ScrollDownButton className="app-select-scroll-btn">▾</RadixSelect.ScrollDownButton>
        </RadixSelect.Content>
      </RadixSelect.Portal>
    </RadixSelect.Root>
  )
}

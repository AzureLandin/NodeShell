import { Fieldset } from '@base-ui/react/fieldset'
import type { ReactNode } from 'react'

export interface FormFieldsetProps extends Omit<Fieldset.Root.Props, 'children'> {
  /** Legend text rendered as a native `<legend>`. Omit for legend-less groups. */
  legend?: ReactNode
  /** Fields grouped under the legend. */
  children: ReactNode
}

/**
 * Group of related controls with a shared legend, built on Base UI
 * `Fieldset`. The rendered markup matches the previous hand-written
 * `<fieldset><legend>…</legend>…</fieldset>` blocks.
 */
export function FormFieldset({ legend, children, ...props }: FormFieldsetProps): React.JSX.Element {
  return (
    <Fieldset.Root {...props}>
      {legend === undefined ? null : <Fieldset.Legend>{legend}</Fieldset.Legend>}
      {children}
    </Fieldset.Root>
  )
}

import { NumberField } from '@base-ui/react/number-field'

export interface FormNumberFieldProps extends Omit<NumberField.Root.Props, 'children'> {
  /** Props forwarded to the inner `<input>` element. */
  inputProps?: NumberField.Input.Props
}

/**
 * Numeric input built on Base UI `NumberField`.
 *
 * The stepper buttons are intentionally not rendered, so the control keeps the
 * look of the previous plain `<input type="number">`. Group separators are
 * disabled by default as well; callers opt in through `format` /
 * `allowOutOfRange` when the raw digits must be preserved.
 */
export function FormNumberField({
  inputProps,
  className,
  ...rootProps
}: FormNumberFieldProps): React.JSX.Element {
  return (
    <NumberField.Root
      className={className ? `app-number-field ${className}` : 'app-number-field'}
      {...rootProps}
    >
      <NumberField.Input data-base-ui-number-field-input {...inputProps} />
    </NumberField.Root>
  )
}

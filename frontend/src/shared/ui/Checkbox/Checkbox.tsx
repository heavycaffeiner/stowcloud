import '@mantine/core/styles/InlineInput.layer.css'
import '@mantine/core/styles/Checkbox.layer.css'
import '@mantine/core/styles/CheckboxIndicator.layer.css'
import { Checkbox } from '@mantine/core'
import { srOnly } from '@/shared/theme'
import * as styles from './Checkbox.css'

export interface StowCheckboxProps {
  checked?: boolean
  indeterminate?: boolean
  label?: string
  /** Keeps the label as the accessible name without showing it. */
  hideLabel?: boolean
  disabled?: boolean
  required?: boolean
  name?: string
  value?: string
  onChange?: (checked: boolean) => void
}

export function StowCheckbox({ checked = false, label, hideLabel = false, onChange, ...rest }: StowCheckboxProps) {
  return (
    <Checkbox
      {...rest}
      checked={checked}
      label={hideLabel ? undefined : label}
      aria-label={hideLabel ? label : undefined}
      onChange={(event) => onChange?.(event.currentTarget.checked)}
      classNames={{ root: styles.root, body: styles.body, label: styles.label }}
    />
  )
}

/** Shows a selection state without taking input or focus; the surrounding control does both. */
export function StowCheckboxIndicator({
  checked,
  indeterminate = false,
  label
}: {
  checked: boolean
  indeterminate?: boolean
  label: string
}) {
  return (
    <div className={styles.indicator}>
      <Checkbox.Indicator checked={checked} indeterminate={indeterminate} />
      <span className={srOnly}>{label}</span>
    </div>
  )
}

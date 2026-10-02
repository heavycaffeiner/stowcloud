import '@mantine/core/styles/InlineInput.layer.css'
import '@mantine/core/styles/Switch.layer.css'
import { Switch } from '@mantine/core'
import * as styles from './Switch.css'

export interface StowSwitchProps {
  checked?: boolean
  label: string
  /** Keeps the label as the accessible name without showing it. */
  hideLabel?: boolean
  disabled?: boolean
  required?: boolean
  name?: string
  value?: string
  /** Receives the proposed state. The switch only moves when `checked` follows. */
  onChange?: (checked: boolean) => void
}

export function StowSwitch({ checked = false, label, hideLabel = false, onChange, ...rest }: StowSwitchProps) {
  return (
    <Switch
      {...rest}
      checked={checked}
      label={hideLabel ? undefined : label}
      aria-label={hideLabel ? label : undefined}
      onChange={(event) => onChange?.(event.currentTarget.checked)}
      classNames={{
        body: styles.body,
        label: styles.label,
        input: styles.input,
        track: styles.track,
        thumb: styles.thumb
      }}
    />
  )
}

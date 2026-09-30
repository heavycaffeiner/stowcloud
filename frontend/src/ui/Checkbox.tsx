import 'mdui/components/checkbox.js'
import { useRef } from 'react'
import * as styles from './Checkbox.css'
import * as utilitiesStyles from './utilities.css'

export interface CheckboxProps {
  checked?: boolean
  indeterminate?: boolean
  label?: string
  hideLabel?: boolean
  disabled?: boolean
  required?: boolean
  name?: string
  value?: string
  onChange?: (checked: boolean) => void
}

interface CheckboxElement extends HTMLElement {
  checked: boolean
}

export function Checkbox({
  checked = false,
  indeterminate = false,
  label,
  hideLabel = false,
  disabled = false,
  required = false,
  name,
  value,
  onChange
}: CheckboxProps) {
  const ref = useRef<CheckboxElement>(null)
  return (
    <label className={styles.root}>
      <mdui-checkbox
        ref={ref}
        className={styles.control}
        checked={checked}
        indeterminate={indeterminate}
        disabled={disabled}
        required={required}
        name={name}
        value={value}
        onChange={() => {
          if (ref.current) onChange?.(ref.current.checked)
        }}
        aria-label={label}
      />
      {label && !hideLabel ? <span className={styles.label}>{label}</span> : null}
    </label>
  )
}

/** Shows a selection state without taking input or focus; the surrounding control does both. */
export function CheckboxIndicator({ checked, label }: { checked: boolean; label: string }) {
  return (
    <mdui-checkbox className={styles.indicator} checked={checked} tabIndex={-1}>
      <span className={utilitiesStyles.srOnly}>{label}</span>
    </mdui-checkbox>
  )
}

import { StowProgressLinear } from '@/shared/ui'
import { scorePasswordStrength } from '../password-strength'
import * as styles from './PasswordStrengthMeter.css'

interface PasswordStrengthMeterProps {
  password: string
  /** The bar's accessible name, built from the tier label. */
  label: (level: string) => string
}

/** A strength bar for a new password; renders nothing until something is typed. */
export function PasswordStrengthMeter({ password, label }: PasswordStrengthMeterProps) {
  if (!password) return null
  const strength = scorePasswordStrength(password)
  return (
    <div className={styles.root}>
      <StowProgressLinear
        className={styles.bar}
        value={strength.ratio}
        tone={strength.tier}
        label={label(strength.label)}
      />
      <span className={styles.label}>{strength.label}</span>
    </div>
  )
}

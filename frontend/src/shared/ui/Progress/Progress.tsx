import '@mantine/core/styles/Loader.layer.css'
import '@mantine/core/styles/Progress.layer.css'
import { Loader, Progress } from '@mantine/core'
import { vars } from '@/shared/theme'
import { useI18n } from '../../../hooks/use-i18n'
import { cx } from '../cx'
import * as styles from './Progress.css'

export interface StowProgressCircularProps {
  size?: 14 | 16 | 24 | 40
  label?: string
  className?: string
}

/** A spinner for work of unknown length. */
export function StowProgressCircular({ size = 24, label, className }: StowProgressCircularProps) {
  const { t } = useI18n()
  return <Loader size={size} role="progressbar" aria-label={label ?? t('progress.loading')} className={className} />
}

const toneColor = {
  primary: vars.color.accent.solid,
  weak: vars.color.danger.solid,
  fair: vars.color.highlight.solid,
  strong: vars.color.accent.solid
}

export interface StowProgressLinearProps {
  /** A fraction from 0 to 1. Undefined, or anything not finite, shows work of unknown length. */
  value?: number | null
  label?: string
  tone?: keyof typeof toneColor
  className?: string
}

export function StowProgressLinear({ value, label, tone = 'primary', className }: StowProgressLinearProps) {
  const { t } = useI18n()
  const known = typeof value === 'number' && Number.isFinite(value)
  const percent = known ? Math.round(Math.min(Math.max(value, 0), 1) * 100) : 100
  return (
    <Progress.Root className={cx(styles.linear, className)}>
      <Progress.Section
        value={percent}
        color={toneColor[tone]}
        animated={!known}
        // Mantine reports a value even for an indeterminate bar, so the role is set here instead.
        withAria={false}
        role="progressbar"
        aria-label={label ?? t('progress.progress')}
        aria-valuemin={known ? 0 : undefined}
        aria-valuemax={known ? 100 : undefined}
        aria-valuenow={known ? percent : undefined}
        className={styles.section}
      />
    </Progress.Root>
  )
}

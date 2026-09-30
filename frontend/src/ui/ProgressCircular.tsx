import 'mdui/components/circular-progress.js'
import { useI18n } from '../hooks/use-i18n'

export interface ProgressCircularProps {
  value?: number | null
  size?: number
  label?: string
  className?: string
}

export function ProgressCircular({ value = null, size = 24, label, className }: ProgressCircularProps) {
  const { t } = useI18n()
  const indeterminate = value === null || typeof value !== 'number' || !Number.isFinite(value)
  const percent = Math.round(Math.min(Math.max(value ?? 0, 0), 1) * 100)
  // mdui gives the element no role, so the host carries the progressbar semantics.
  return (
    <mdui-circular-progress
      className={className}
      value={indeterminate ? undefined : percent / 100}
      max={1}
      style={{ width: size, height: size }}
      role="progressbar"
      aria-label={label ?? t('progress.loading')}
      aria-valuemin={indeterminate ? undefined : 0}
      aria-valuemax={indeterminate ? undefined : 100}
      aria-valuenow={indeterminate ? undefined : percent}
    />
  )
}

import 'mdui/components/linear-progress.js'
import { useI18n } from '../hooks/use-i18n'

export interface ProgressLinearProps {
  value?: number | null
  label?: string
  tone?: 'primary' | 'weak' | 'fair' | 'strong'
}

export function ProgressLinear({ value = null, label, tone = 'primary' }: ProgressLinearProps) {
  const { t } = useI18n()
  const indeterminate = value === null || typeof value !== 'number' || !Number.isFinite(value)
  const fraction = Math.min(Math.max(value ?? 0, 0), 1)
  const percent = Math.round(fraction * 100)
  // mdui gives the element no role, so the host carries the progressbar semantics.
  return (
    <div className={`sc-progress-linear sc-progress-linear-${tone}`}>
      <mdui-linear-progress
        value={indeterminate ? undefined : fraction}
        max={1}
        role="progressbar"
        aria-label={label ?? t('progress.progress')}
        aria-valuemin={indeterminate ? undefined : 0}
        aria-valuemax={indeterminate ? undefined : 100}
        aria-valuenow={indeterminate ? undefined : percent}
      />
    </div>
  )
}

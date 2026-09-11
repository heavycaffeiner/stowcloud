import { useI18n } from '../i18n/use-i18n'

export interface ProgressLinearProps {
  value?: number | null
  label?: string
  tone?: 'primary' | 'weak' | 'fair' | 'strong'
}

export function ProgressLinear({ value = null, label, tone = 'primary' }: ProgressLinearProps) {
  const { t } = useI18n()
  const indeterminate = value === null || typeof value !== 'number' || !Number.isFinite(value)
  const fraction = Math.min(Math.max(value ?? 0, 0), 1)
  const resolved = label ?? t('progress.progress')
  return (
    <div className={`sc-progress-linear sc-progress-linear--${tone}`}>
      <mdui-linear-progress value={indeterminate ? undefined : fraction} max={1} aria-label={resolved} />
    </div>
  )
}

import { useEffect, useRef } from 'react'
import { useI18n } from '../i18n/use-i18n'

interface ProgressElement extends HTMLElement {
  updateComplete?: Promise<unknown>
}

function syncProgressLabel(element: ProgressElement | null, label: string): void {
  if (!element) return
  const apply = () => {
    const progress = element.shadowRoot?.querySelector<HTMLElement>('svg, [role="progressbar"]')
    if (progress) progress.setAttribute('aria-label', label)
  }
  if (element.updateComplete) void element.updateComplete.then(apply)
  else queueMicrotask(apply)
}
export interface ProgressCircularProps {
  value?: number | null
  size?: number
  label?: string
}

export function ProgressCircular({ value = null, size = 24, label }: ProgressCircularProps) {
  const { t } = useI18n()
  const indeterminate = value === null || typeof value !== 'number' || !Number.isFinite(value)
  const percent = Math.round(Math.min(Math.max(value ?? 0, 0), 1) * 100)
  const resolved = label ?? t('progress.loading')
  const ref = useRef<ProgressElement | null>(null)
  useEffect(() => { syncProgressLabel(ref.current, resolved) }, [resolved])
  return (
    <mdui-circular-progress
      ref={ref}
      value={indeterminate ? undefined : percent / 100}
      max={1}
      style={{ width: size, height: size }}
      aria-label={resolved}
    />
  )
}

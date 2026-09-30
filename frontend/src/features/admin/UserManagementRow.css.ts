import { style } from '@vanilla-extract/css'
import { vars } from '../../ui/theme.css'

export const chipMuted = style({
  background: `rgb(${vars.color.surfaceContainerHighest})`,
  color: `rgb(${vars.color.onSurfaceVariant})`
})

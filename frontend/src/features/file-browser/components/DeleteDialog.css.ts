import { style } from '@vanilla-extract/css'
import { vars } from '@/shared/theme'

export const externalWarning = style({
  display: 'flex',
  alignItems: 'center',
  gap: vars.space.sm,
  color: vars.color.highlight.solid
})

import { style } from '@vanilla-extract/css'
import { vars } from '@/shared/theme'

export const chipMuted = style({
  background: vars.color.surface.fill,
  color: vars.color.text.secondary
})

import { style } from '@vanilla-extract/css'
import { vars } from '@/shared/theme'

export const body = style({
  display: 'flex',
  flexDirection: 'column',
  gap: vars.space.lg,
  minWidth: 0
})

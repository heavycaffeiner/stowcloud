import { style } from '@vanilla-extract/css'
import { vars } from '@/shared/theme'

export const form = style({
  display: 'flex',
  flexDirection: 'column',
  alignItems: 'flex-start',
  gap: vars.space.lg,
  width: '100%',
  maxWidth: '32rem',
  minWidth: 0
})

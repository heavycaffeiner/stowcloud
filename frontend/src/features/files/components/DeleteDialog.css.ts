import { style } from '@vanilla-extract/css'
import { vars } from '@/shared/theme'

export const externalWarning = style({
  display: 'flex',
  alignItems: 'center',
  gap: '8px',
  color: vars.color.highlight.solid
})

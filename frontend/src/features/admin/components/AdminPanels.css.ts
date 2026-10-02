import { style } from '@vanilla-extract/css'
import { typography, vars } from '@/shared/theme'

export const pageSection = style({
  minWidth: 0
})

export const pageTitle = style({
  margin: `0 0 ${vars.space.sm}`,
  color: vars.color.text.primary,
  ...typography('titleLarge')
})

export const loading = style({
  display: 'flex',
  alignItems: 'center',
  gap: vars.space.md,
  color: vars.color.text.secondary
})

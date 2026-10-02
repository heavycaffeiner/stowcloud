import { style } from '@vanilla-extract/css'
import { vars } from '@/shared/theme'

export const body = style({
  display: 'flex',
  flexDirection: 'column',
  gap: vars.space.lg,
  minInlineSize: 0
})

export const error = style({
  margin: 0,
  color: vars.color.danger.solid,
  overflowWrap: 'anywhere'
})

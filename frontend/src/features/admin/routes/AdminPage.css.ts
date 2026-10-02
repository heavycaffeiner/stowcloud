import { style } from '@vanilla-extract/css'
import { vars } from '@/shared/theme'

export const inner = style({
  display: 'grid',
  gap: 0,
  minWidth: 0
})

export const denied = style({
  margin: 0,
  color: vars.color.text.secondary,
  overflowWrap: 'anywhere'
})

export const pageError = style({
  margin: 0,
  color: vars.color.danger.solid,
  overflowWrap: 'anywhere'
})

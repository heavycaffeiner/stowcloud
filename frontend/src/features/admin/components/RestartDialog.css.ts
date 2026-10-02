import { style } from '@vanilla-extract/css'
import { vars } from '@/shared/theme'

export const status = style({
  display: 'inline-flex',
  alignItems: 'center',
  flexWrap: 'wrap',
  gap: '12px',
  maxWidth: '100%',
  overflowWrap: 'anywhere'
})

export const error = style({
  color: vars.color.danger.solid,
  overflowWrap: 'anywhere'
})

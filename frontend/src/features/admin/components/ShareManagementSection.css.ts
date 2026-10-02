import { style } from '@vanilla-extract/css'
import { vars } from '@/shared/theme'

export const encAnnounce = style({
  minHeight: '1.25em',
  margin: 0,
  color: vars.color.accent.solid,
  overflowWrap: 'anywhere'
})

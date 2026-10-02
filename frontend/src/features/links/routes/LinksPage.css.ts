import { style } from '@vanilla-extract/css'
import { typography, vars } from '@/shared/theme'

export const item = style({
  minWidth: 0
})

export const targetError = style({
  margin: `${vars.space.xs} ${vars.space.lg} ${vars.space.md}`,
  padding: `${vars.space.sm} ${vars.space.md}`,
  overflowWrap: 'anywhere',
  borderRadius: vars.radius.sm,
  background: vars.color.danger.soft,
  color: vars.color.danger.onSoft,
  ...typography('bodySmall')
})

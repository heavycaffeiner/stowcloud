import { style } from '@vanilla-extract/css'
import { typography, vars } from '@/shared/theme'

export const facts = style({
  display: 'flex',
  flexDirection: 'column',
  gap: vars.space.sm,
  margin: `0 0 ${vars.space.lg}`
})

export const factLabel = style({
  color: vars.color.text.secondary,
  ...typography('bodySmall')
})

export const factValue = style({
  margin: 0,
  overflowWrap: 'anywhere'
})

export const hint = style({
  margin: `0 0 ${vars.space.sm}`,
  color: vars.color.text.secondary,
  ...typography('bodySmall'),
  lineHeight: '1.45'
})

export const error = style({
  margin: 0,
  color: vars.color.danger.solid,
  ...typography('bodySmall'),
  lineHeight: '1.45'
})

export const warning = style({
  display: 'flex',
  alignItems: 'flex-start',
  gap: vars.space.sm,
  margin: `0 0 ${vars.space.lg}`,
  padding: vars.space.md,
  borderRadius: vars.radius.xs,
  background: vars.color.danger.soft,
  color: vars.color.danger.onSoft
})

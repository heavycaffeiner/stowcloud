import { style } from '@vanilla-extract/css'
import { typography, vars } from '@/shared/theme'

export const supporting = style({
  display: 'flex',
  flexDirection: 'column',
  gap: vars.space.xs,
  minWidth: 0,
  color: vars.color.text.secondary,
  ...typography('bodySmall'),
  overflowWrap: 'anywhere'
})

export const summaryDeny = style({
  color: vars.color.danger.solid
})

export const warning = style({
  display: 'flex',
  alignItems: 'flex-start',
  gap: vars.space.xs,
  color: vars.color.danger.solid,
  ...typography('bodySmall'),
  overflowWrap: 'anywhere'
})

export const perms = style({
  display: 'flex',
  flexWrap: 'wrap',
  gap: vars.space.xs
})

export const chevron = style({
  display: 'inline-flex',
  transition: `transform ${vars.motion.short} ${vars.motion.easing}`
})

export const chevronOpen = style({
  transform: 'rotate(90deg)'
})

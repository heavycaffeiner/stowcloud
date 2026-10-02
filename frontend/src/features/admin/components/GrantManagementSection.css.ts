import { style } from '@vanilla-extract/css'
import { vars } from '@/shared/theme'

export const supporting = style({
  display: 'flex',
  flexDirection: 'column',
  gap: '4px',
  minWidth: '0',
  color: vars.color.text.secondary,
  fontSize: vars.typography.bodySmall.size,
  overflowWrap: 'anywhere'
})

export const summaryDeny = style({
  color: vars.color.danger.solid
})

export const warning = style({
  display: 'flex',
  alignItems: 'flex-start',
  gap: '4px',
  color: vars.color.danger.solid,
  fontSize: vars.typography.bodySmall.size,
  overflowWrap: 'anywhere'
})

export const perms = style({
  display: 'flex',
  flexWrap: 'wrap',
  gap: '4px'
})

export const chevron = style({
  display: 'inline-flex',
  transition: 'transform 150ms ease'
})

export const chevronOpen = style({
  transform: 'rotate(90deg)'
})

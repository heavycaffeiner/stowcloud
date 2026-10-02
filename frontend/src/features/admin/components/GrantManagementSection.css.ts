import { style } from '@vanilla-extract/css'
import { vars } from '../../../ui/theme.css'

export const supporting = style({
  display: 'flex',
  flexDirection: 'column',
  gap: '4px',
  minWidth: '0',
  color: `rgb(${vars.color.onSurfaceVariant})`,
  fontSize: vars.typescale.bodySmall.size,
  overflowWrap: 'anywhere'
})

export const summaryDeny = style({
  color: `rgb(${vars.color.error})`
})

export const warning = style({
  display: 'flex',
  alignItems: 'flex-start',
  gap: '4px',
  color: `rgb(${vars.color.error})`,
  fontSize: vars.typescale.bodySmall.size,
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

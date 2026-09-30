import { style } from '@vanilla-extract/css'
import { vars } from './theme.css'

export const notice = style({
  display: 'flex',
  flexWrap: 'wrap',
  alignItems: 'center',
  justifyContent: 'space-between',
  gap: '8px 16px',
  padding: '12px 16px',
  borderRadius: vars.radius.small,
  border: `1px solid ${vars.outline.variant}`,
  background: vars.surface.raised,
  color: `rgb(${vars.color.onSurface})`
})

export const message = style({
  margin: '0',
  overflowWrap: 'anywhere'
})

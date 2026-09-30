import { style } from '@vanilla-extract/css'
import { vars } from '../../ui/theme.css'

export const facts = style({
  display: 'flex',
  flexDirection: 'column',
  gap: '8px',
  margin: '0 0 16px'
})

export const factLabel = style({
  color: `rgb(${vars.color.onSurfaceVariant})`,
  fontSize: vars.typescale.bodySmall.size
})

export const factValue = style({
  margin: '0',
  overflowWrap: 'anywhere'
})

export const hint = style({
  margin: '0 0 8px',
  color: `rgb(${vars.color.onSurfaceVariant})`,
  fontSize: vars.typescale.bodySmall.size,
  lineHeight: '1.45'
})

export const error = style({
  margin: '0',
  color: `rgb(${vars.color.error})`,
  fontSize: vars.typescale.bodySmall.size,
  lineHeight: '1.45'
})

export const warning = style({
  display: 'flex',
  alignItems: 'flex-start',
  gap: '8px',
  margin: '0 0 16px',
  padding: '12px',
  borderRadius: vars.shape.cornerExtraSmall,
  background: `rgb(${vars.color.errorContainer})`,
  color: `rgb(${vars.color.onErrorContainer})`
})

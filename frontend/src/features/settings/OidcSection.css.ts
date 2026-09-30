import { style } from '@vanilla-extract/css'
import { vars } from '../../ui/theme.css'

export const error = style({
  margin: '0',
  color: `rgb(${vars.color.onSurfaceVariant})`,
  overflowWrap: 'anywhere'
})

export const root = style({
  display: 'flex',
  flexDirection: 'column',
  gap: '16px',
  minWidth: '0'
})

export const status = style({
  display: 'flex',
  alignItems: 'center',
  flexWrap: 'wrap',
  gap: '16px'
})

export const badge = style({
  display: 'inline-flex',
  alignItems: 'center',
  minBlockSize: '24px',
  paddingInline: '8px',
  borderRadius: vars.shape.cornerFull,
  background: `rgb(${vars.color.surfaceContainerHighest})`,
  color: `rgb(${vars.color.onSurfaceVariant})`,
  fontSize: vars.typescale.labelSmall.size,
  lineHeight: vars.typescale.labelSmall.lineHeight
})

export const badgeOn = style({
  background: `rgb(${vars.color.primaryContainer})`,
  color: `rgb(${vars.color.onPrimaryContainer})`
})

export const warning = style({
  display: 'flex',
  alignItems: 'flex-start',
  gap: '8px',
  margin: '0',
  padding: '12px',
  borderRadius: vars.shape.cornerExtraSmall,
  background: `rgb(${vars.color.errorContainer})`,
  color: `rgb(${vars.color.onErrorContainer})`,
  overflowWrap: 'anywhere'
})

export const detail = style({
  margin: '0',
  color: `rgb(${vars.color.onSurfaceVariant})`,
  overflowWrap: 'anywhere'
})

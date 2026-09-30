import { style } from '@vanilla-extract/css'
import { vars } from '../../../ui/theme.css'

export const pageSection = style({
  minWidth: '0'
})

export const pageTitle = style({
  margin: '0 0 8px',
  color: `rgb(${vars.color.onSurface})`,
  fontSize: vars.typescale.titleLarge.size,
  fontWeight: vars.typescale.titleLarge.weight,
  letterSpacing: vars.typescale.titleLarge.tracking,
  lineHeight: vars.typescale.titleLarge.lineHeight
})

export const loading = style({
  display: 'flex',
  alignItems: 'center',
  gap: '12px',
  color: `rgb(${vars.color.onSurfaceVariant})`
})

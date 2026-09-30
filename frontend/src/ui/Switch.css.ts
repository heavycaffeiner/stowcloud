import { style } from '@vanilla-extract/css'
import { vars } from './theme.css'

export const root = style({
  display: 'inline-flex',
  alignItems: 'center',
  gap: '12px',
  minHeight: '48px',
  color: `rgb(${vars.color.onSurface})`,
  fontSize: vars.typescale.bodyLarge.size,
  fontWeight: vars.typescale.bodyLarge.weight,
  lineHeight: vars.typescale.bodyLarge.lineHeight,
  cursor: 'pointer',
  flexShrink: '0'
})

export const control = style({
  flex: 'none'
})

export const label = style({
  minWidth: '0'
})

export const disabled = style({
  cursor: 'not-allowed',
  opacity: '0.38'
})

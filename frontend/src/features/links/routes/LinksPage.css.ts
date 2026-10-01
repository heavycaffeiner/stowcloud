import { style } from '@vanilla-extract/css'
import { vars } from '../../../ui/theme.css'

export const item = style({
  minWidth: '0'
})

export const targetError = style({
  margin: '4px 16px 12px',
  padding: '8px 12px',
  overflowWrap: 'anywhere',
  borderRadius: '8px',
  background: vars.state.error,
  color: vars.state.errorContent,
  fontSize: vars.typescale.bodySmall.size,
  fontWeight: vars.typescale.bodySmall.weight,
  lineHeight: vars.typescale.bodySmall.lineHeight,
  letterSpacing: vars.typescale.bodySmall.tracking
})

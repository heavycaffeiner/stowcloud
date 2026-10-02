import { style } from '@vanilla-extract/css'
import { vars } from '@/shared/theme'

export const item = style({
  minWidth: '0'
})

export const targetError = style({
  margin: '4px 16px 12px',
  padding: '8px 12px',
  overflowWrap: 'anywhere',
  borderRadius: '8px',
  background: vars.color.danger.soft,
  color: vars.color.danger.onSoft,
  fontSize: vars.typography.bodySmall.size,
  fontWeight: vars.typography.bodySmall.weight,
  lineHeight: vars.typography.bodySmall.lineHeight,
  letterSpacing: vars.typography.bodySmall.tracking
})

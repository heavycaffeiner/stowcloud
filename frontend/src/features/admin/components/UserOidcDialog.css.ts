import { style } from '@vanilla-extract/css'
import { vars } from '@/shared/theme'

export const facts = style({
  display: 'flex',
  flexDirection: 'column',
  gap: '8px',
  margin: '0 0 16px'
})

export const factLabel = style({
  color: vars.color.text.secondary,
  fontSize: vars.typography.bodySmall.size
})

export const factValue = style({
  margin: '0',
  overflowWrap: 'anywhere'
})

export const hint = style({
  margin: '0 0 8px',
  color: vars.color.text.secondary,
  fontSize: vars.typography.bodySmall.size,
  lineHeight: '1.45'
})

export const error = style({
  margin: '0',
  color: vars.color.danger.solid,
  fontSize: vars.typography.bodySmall.size,
  lineHeight: '1.45'
})

export const warning = style({
  display: 'flex',
  alignItems: 'flex-start',
  gap: '8px',
  margin: '0 0 16px',
  padding: '12px',
  borderRadius: vars.radius.xs,
  background: vars.color.danger.soft,
  color: vars.color.danger.onSoft
})

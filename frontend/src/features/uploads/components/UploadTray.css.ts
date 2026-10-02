import { style } from '@vanilla-extract/css'
import { vars } from '@/shared/theme'

export const root = style({
  width: 'min(360px, calc(100vw - 32px))',
  maxHeight: '60vh',
  display: 'flex',
  flexDirection: 'column',
  overflow: 'hidden',
  borderRadius: '16px',
  background: vars.color.surface.overlay,
  color: vars.color.text.primary,
  boxShadow: '0 8px 24px rgba(0, 0, 0, 0.24)',
  border: `1px solid ${vars.color.border.subtle}`
})

export const header = style({
  minHeight: '48px',
  display: 'flex',
  alignItems: 'center',
  justifyContent: 'space-between',
  paddingInline: '16px'
})

export const title = style({
  display: 'inline-flex',
  alignItems: 'center',
  gap: '8px',
  border: '0',
  background: 'transparent',
  color: 'inherit',
  cursor: 'pointer',
  font: 'inherit',
  fontSize: vars.typography.label.size,
  fontWeight: vars.typography.label.weight,
  lineHeight: vars.typography.label.lineHeight
})

export const actions = style({
  display: 'flex',
  alignItems: 'center',
  gap: '4px'
})

export const controls = style({
  display: 'flex',
  alignItems: 'center',
  gap: '4px'
})

export const list = style({
  listStyle: 'none',
  margin: '0',
  padding: '8px 16px'
})

export const scroll = style({
  minHeight: '0',
  overflowY: 'auto'
})

export const item = style({
  paddingBlock: '10px',
  borderBottom: `1px solid ${vars.color.border.subtle}`
})

export const row = style({
  display: 'flex',
  justifyContent: 'space-between',
  gap: '12px',
  marginBottom: '4px'
})

export const name = style({
  minWidth: '0',
  display: 'inline-flex',
  alignItems: 'center',
  gap: '4px',
  overflowWrap: 'anywhere',
  fontSize: vars.typography.body.size,
  lineHeight: vars.typography.body.lineHeight
})

export const meta = style({
  flexShrink: '0',
  color: vars.color.text.secondary,
  fontSize: vars.typography.bodySmall.size,
  lineHeight: vars.typography.bodySmall.lineHeight
})

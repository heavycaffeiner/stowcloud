import { style } from '@vanilla-extract/css'
import { vars } from '../../ui/theme.css'

export const root = style({
  width: 'min(360px, calc(100vw - 32px))',
  maxHeight: '60vh',
  display: 'flex',
  flexDirection: 'column',
  overflow: 'hidden',
  borderRadius: '16px',
  background: vars.surface.overlay,
  color: vars.content.primary,
  boxShadow: '0 8px 24px rgba(0, 0, 0, 0.24)',
  border: `1px solid ${vars.outline.variant}`
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
  fontSize: vars.typescale.labelLarge.size,
  fontWeight: vars.typescale.labelLarge.weight,
  lineHeight: vars.typescale.labelLarge.lineHeight
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
  borderBottom: `1px solid ${vars.outline.variant}`
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
  fontSize: vars.typescale.bodyMedium.size,
  lineHeight: vars.typescale.bodyMedium.lineHeight
})

export const meta = style({
  flexShrink: '0',
  color: vars.content.secondary,
  fontSize: vars.typescale.bodySmall.size,
  lineHeight: vars.typescale.bodySmall.lineHeight
})

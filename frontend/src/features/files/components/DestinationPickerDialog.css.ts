import { style } from '@vanilla-extract/css'
import { vars } from '@/shared/theme'

export const root = style({
  minWidth: 'min(360px, 72vw)'
})

export const prompt = style({
  margin: '0 0 8px',
  fontSize: vars.typography.body.size,
  lineHeight: vars.typography.body.lineHeight
})

export const tree = style({
  maxHeight: '40vh',
  overflowY: 'auto',
  padding: '4px',
  border: 'none',
  background: vars.color.surface.overlay,
  borderRadius: vars.radius.sm
})

export const status = style({
  minHeight: '20px',
  margin: '8px 0 0',
  color: vars.color.text.secondary,
  overflowWrap: 'anywhere'
})

export const statusWarn = style({
  minHeight: '20px',
  margin: '8px 0 0',
  color: vars.color.danger.solid,
  overflowWrap: 'anywhere'
})

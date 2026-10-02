import { style } from '@vanilla-extract/css'
import { vars } from '@/shared/theme'

export const root = style({
  display: 'flex',
  minHeight: '0',
  height: '100%',
  minWidth: '0',
  overflow: 'hidden',
  padding: vars.layout.pagePad
})

export const inner = style({
  display: 'flex',
  flexDirection: 'column',
  gap: '12px',
  width: '100%',
  minWidth: '0',
  minHeight: '0'
})

export const header = style({
  display: 'flex',
  alignItems: 'center',
  gap: '8px'
})

export const title = style({
  margin: '0',
  fontSize: vars.typography.heading.size,
  fontWeight: vars.typography.heading.weight,
  lineHeight: vars.typography.heading.lineHeight,
  letterSpacing: vars.typography.heading.tracking
})

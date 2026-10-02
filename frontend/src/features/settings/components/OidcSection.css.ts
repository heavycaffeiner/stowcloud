import { style } from '@vanilla-extract/css'
import { vars } from '@/shared/theme'

export const error = style({
  margin: '0',
  color: vars.color.text.secondary,
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
  borderRadius: vars.radius.full,
  background: vars.color.surface.fill,
  color: vars.color.text.secondary,
  fontSize: vars.typography.caption.size,
  lineHeight: vars.typography.caption.lineHeight
})

export const badgeOn = style({
  background: vars.color.accent.soft,
  color: vars.color.accent.onSoft
})

export const warning = style({
  display: 'flex',
  alignItems: 'flex-start',
  gap: '8px',
  margin: '0',
  padding: '12px',
  borderRadius: vars.radius.xs,
  background: vars.color.danger.soft,
  color: vars.color.danger.onSoft,
  overflowWrap: 'anywhere'
})

export const detail = style({
  margin: '0',
  color: vars.color.text.secondary,
  overflowWrap: 'anywhere'
})

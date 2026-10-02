import { style } from '@vanilla-extract/css'
import { vars } from '@/shared/theme'

export const copyFeedback = style({
  margin: '0',
  color: vars.color.danger.solid,
  overflowWrap: 'anywhere'
})

export const codes = style({
  display: 'grid',
  gridTemplateColumns: 'repeat(2, minmax(0, 1fr))',
  gap: '8px',
  listStyle: 'none',
  margin: '0',
  padding: '0',
  '@media': {
    '(max-width: 599.98px)': {
      gridTemplateColumns: '1fr'
    }
  }
})

export const code = style({
  width: '100%',
  boxSizing: 'border-box',
  padding: '8px 12px',
  minHeight: '40px',
  height: '40px',
  lineHeight: '24px',
  border: '0',
  borderRadius: vars.radius.xs,
  background: vars.color.surface.fill,
  color: vars.color.text.primary,
  font: 'inherit',
  overflowWrap: 'anywhere',
  selectors: {
    '&:focus-visible': {
      outline: `${vars.focusRing.width} solid ${vars.color.focus}`,
      outlineOffset: vars.focusRing.offset
    }
  }
})

export const secret = style({
  width: '100%',
  boxSizing: 'border-box',
  padding: '8px 12px',
  minHeight: '40px',
  height: '40px',
  lineHeight: '24px',
  border: '0',
  borderRadius: vars.radius.xs,
  background: vars.color.surface.fill,
  color: vars.color.text.primary,
  font: 'inherit',
  overflowWrap: 'anywhere',
  selectors: {
    '&:focus-visible': {
      outline: `${vars.focusRing.width} solid ${vars.color.focus}`,
      outlineOffset: vars.focusRing.offset
    }
  }
})

export const secretRow = style({
  display: 'flex',
  alignItems: 'center',
  gap: '8px',
  minWidth: '0',
  '@media': {
    '(max-width: 599.98px)': {
      alignItems: 'stretch'
    }
  }
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

export const recovery = style({
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

export const recoveryCount = style({
  margin: '0',
  color: vars.color.text.secondary,
  overflowWrap: 'anywhere'
})

export const recoveryCountLow = style({
  color: vars.color.danger.solid,
  fontWeight: '500'
})

export const smbWarning = style({
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

export const url = style({
  margin: '0',
  color: vars.color.text.secondary,
  overflowWrap: 'anywhere'
})

import { style } from '@vanilla-extract/css'
import { vars } from '@/shared/theme'

export const root = style({
  boxSizing: 'border-box',
  flex: 'none',
  width: '340px',
  alignSelf: 'stretch',
  padding: '12px 16px 24px',
  borderInlineStart: `1px solid ${vars.color.border.subtle}`,
  background: vars.color.surface.container,
  color: vars.color.text.primary,
  overflowY: 'auto'
})

export const sheet = style({
  position: 'fixed',
  inset: '0',
  zIndex: '30',
  width: 'auto',
  borderInlineStart: '0',
  background: vars.color.surface.raised,
  paddingBottom: 'calc(16px + env(safe-area-inset-bottom, 0px))'
})

export const head = style({
  display: 'flex',
  alignItems: 'center',
  gap: '8px',
  minHeight: '48px',
  marginBottom: '12px'
})

export const headIcon = style({
  display: 'inline-flex',
  flex: 'none',
  color: vars.color.text.icon
})

export const title = style({
  flex: '1',
  minWidth: '0',
  margin: '0',
  fontSize: vars.typography.title.size,
  lineHeight: vars.typography.title.lineHeight,
  fontWeight: '600',
  overflow: 'hidden',
  textOverflow: 'ellipsis',
  whiteSpace: 'nowrap'
})

export const summary = style({
  display: 'flex',
  alignItems: 'center',
  gap: '12px',
  minWidth: '0',
  padding: '8px 0 16px'
})

export const summaryIcon = style({
  display: 'inline-flex',
  flex: 'none',
  color: vars.color.text.icon
})

export const summaryTitle = style({
  fontSize: vars.typography.titleSmall.size,
  lineHeight: vars.typography.titleSmall.lineHeight,
  fontWeight: '600',
  overflowWrap: 'anywhere'
})

export const summaryDesc = style({
  color: vars.color.text.secondary,
  fontSize: vars.typography.bodySmall.size,
  lineHeight: vars.typography.bodySmall.lineHeight
})

export const actions = style({
  display: 'flex',
  alignItems: 'center',
  gap: '8px',
  marginBottom: '18px'
})

export const action = style({
  flex: '1'
})

export const sectionHeading = style({
  fontSize: vars.typography.titleSmall.size,
  lineHeight: vars.typography.titleSmall.lineHeight,
  fontWeight: '600',
  color: vars.color.text.secondary,
  margin: '12px 0 8px'
})

export const fields = style({
  display: 'flex',
  flexDirection: 'column',
  gap: '12px',
  margin: '0',
  padding: '0'
})

export const field = style({
  display: 'flex',
  flexDirection: 'column',
  gap: '2px'
})

export const fieldLabel = style({
  color: vars.color.text.secondary,
  fontSize: vars.typography.labelSmall.size,
  lineHeight: vars.typography.labelSmall.lineHeight,
  fontWeight: '500',
  margin: '0'
})

export const fieldValue = style({
  color: vars.color.text.primary,
  fontSize: vars.typography.body.size,
  lineHeight: vars.typography.body.lineHeight,
  fontWeight: '400',
  margin: '0',
  overflowWrap: 'anywhere'
})

export const fieldNote = style({
  display: 'block',
  color: vars.color.text.secondary,
  fontSize: vars.typography.bodySmall.size,
  lineHeight: vars.typography.bodySmall.lineHeight,
  marginTop: '2px'
})

import { style } from '@vanilla-extract/css'
import { vars } from '../../ui/theme.css'

export const root = style({
  boxSizing: 'border-box',
  flex: 'none',
  width: '340px',
  alignSelf: 'stretch',
  padding: '12px 16px 24px',
  borderInlineStart: `1px solid ${vars.outline.variant}`,
  background: vars.surface.container,
  color: vars.content.primary,
  overflowY: 'auto'
})

export const sheet = style({
  position: 'fixed',
  inset: '0',
  zIndex: '30',
  width: 'auto',
  borderInlineStart: '0',
  background: vars.surface.raised,
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
  flex: 'none'
})

export const title = style({
  flex: '1',
  minWidth: '0',
  margin: '0',
  fontSize: vars.typescale.titleMedium.size,
  lineHeight: vars.typescale.titleMedium.lineHeight,
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
  flex: 'none'
})

export const summaryTitle = style({
  fontSize: vars.typescale.titleSmall.size,
  lineHeight: vars.typescale.titleSmall.lineHeight,
  fontWeight: '600',
  overflowWrap: 'anywhere'
})

export const summaryDesc = style({
  color: vars.content.secondary,
  fontSize: vars.typescale.bodySmall.size,
  lineHeight: vars.typescale.bodySmall.lineHeight
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
  fontSize: vars.typescale.titleSmall.size,
  lineHeight: vars.typescale.titleSmall.lineHeight,
  fontWeight: '600',
  color: vars.content.secondary,
  margin: '12px 0 8px'
})

export const warning = style({
  display: 'flex',
  alignItems: 'center',
  gap: '8px',
  margin: '0 0 12px',
  padding: '8px 12px',
  borderRadius: vars.radius.small,
  background: vars.state.error,
  color: vars.state.errorContent,
  fontSize: vars.typescale.bodySmall.size,
  lineHeight: vars.typescale.bodySmall.lineHeight
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
  color: vars.content.secondary,
  fontSize: vars.typescale.labelMedium.size,
  lineHeight: vars.typescale.labelMedium.lineHeight,
  fontWeight: '500',
  margin: '0'
})

export const fieldValue = style({
  color: vars.content.primary,
  fontSize: vars.typescale.bodyMedium.size,
  lineHeight: vars.typescale.bodyMedium.lineHeight,
  fontWeight: '400',
  margin: '0',
  overflowWrap: 'anywhere'
})

export const fieldNote = style({
  display: 'block',
  color: vars.content.secondary,
  fontSize: vars.typescale.bodySmall.size,
  lineHeight: vars.typescale.bodySmall.lineHeight,
  marginTop: '2px'
})

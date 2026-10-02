import { style } from '@vanilla-extract/css'
import { typography, vars } from '@/shared/theme'

export const root = style({
  boxSizing: 'border-box',
  flex: 'none',
  width: '340px',
  alignSelf: 'stretch',
  padding: `${vars.space.md} ${vars.space.lg} ${vars.space.xl}`,
  borderInlineStart: `${vars.stroke.thin} solid ${vars.color.border.subtle}`,
  background: vars.color.surface.container,
  color: vars.color.text.primary,
  overflowY: 'auto'
})

export const sheet = style({
  position: 'fixed',
  inset: 0,
  zIndex: 30,
  width: 'auto',
  borderInlineStart: 0,
  background: vars.color.surface.raised,
  paddingBottom: `calc(${vars.space.lg} + env(safe-area-inset-bottom, 0px))`
})

export const head = style({
  display: 'flex',
  alignItems: 'center',
  gap: vars.space.sm,
  minHeight: vars.density.row,
  marginBottom: vars.space.md
})

export const headIcon = style({
  display: 'inline-flex',
  flex: 'none',
  color: vars.color.text.icon
})

export const title = style({
  flex: 1,
  minWidth: 0,
  margin: 0,
  ...typography('title'),
  fontWeight: vars.font.weight.bold,
  overflow: 'hidden',
  textOverflow: 'ellipsis',
  whiteSpace: 'nowrap'
})

export const summary = style({
  display: 'flex',
  alignItems: 'center',
  gap: vars.space.md,
  minWidth: 0,
  padding: `${vars.space.sm} 0 ${vars.space.lg}`
})

export const summaryIcon = style({
  display: 'inline-flex',
  flex: 'none',
  color: vars.color.text.icon
})

export const summaryTitle = style({
  ...typography('titleSmall'),
  fontWeight: vars.font.weight.bold,
  overflowWrap: 'anywhere'
})

export const summaryDesc = style({
  color: vars.color.text.secondary,
  ...typography('bodySmall')
})

export const actions = style({
  display: 'flex',
  alignItems: 'center',
  gap: vars.space.sm,
  marginBottom: vars.space.lg
})

export const action = style({
  flex: 1
})

export const sectionHeading = style({
  ...typography('titleSmall'),
  fontWeight: vars.font.weight.bold,
  color: vars.color.text.secondary,
  margin: `${vars.space.md} 0 ${vars.space.sm}`
})

export const fields = style({
  display: 'flex',
  flexDirection: 'column',
  gap: vars.space.md,
  margin: 0,
  padding: 0
})

export const field = style({
  display: 'flex',
  flexDirection: 'column',
  gap: vars.space.xxs
})

export const fieldLabel = style({
  color: vars.color.text.secondary,
  ...typography('labelSmall'),
  margin: 0
})

export const fieldValue = style({
  color: vars.color.text.primary,
  ...typography('body'),
  margin: 0,
  overflowWrap: 'anywhere'
})

export const fieldNote = style({
  display: 'block',
  color: vars.color.text.secondary,
  ...typography('bodySmall'),
  marginTop: vars.space.xxs
})

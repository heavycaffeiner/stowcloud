import { style } from '@vanilla-extract/css'
import { iconButtonSize } from '@/shared/ui/IconButton/IconButton.css'
import { focusOutline, typography, vars } from '@/shared/theme'

export const root = style({
  display: 'flex',
  flex: 1,
  flexDirection: 'column',
  minWidth: 0,
  minHeight: 0,
  background: vars.color.surface.raised,
  color: vars.color.text.primary,
  vars: {
    [iconButtonSize]: vars.density.controlCompact
  }
})

export const bar = style({
  display: 'flex',
  flex: 'none',
  alignItems: 'center',
  gap: vars.space.sm,
  padding: vars.space.lg,
  background: vars.color.surface.overlay
})

export const iconButton = style({
  borderRadius: vars.radius.full,
  background: vars.color.surface.fill
})

export const meta = style({
  display: 'flex',
  flex: 1,
  flexDirection: 'column',
  gap: vars.space.xxs,
  minWidth: 0
})

export const name = style({
  overflow: 'hidden',
  textOverflow: 'ellipsis',
  whiteSpace: 'nowrap',
  ...typography('title')
})

export const size = style({
  color: vars.color.text.secondary,
  ...typography('bodySmall')
})

export const body = style({
  display: 'flex',
  flex: 1,
  minWidth: 0,
  minHeight: 0,
  padding: vars.space.lg
})

export const nav = style({
  display: 'flex',
  flex: 'none',
  justifyContent: 'space-between',
  gap: vars.space.sm,
  padding: `0 ${vars.space.lg} ${vars.space.lg}`
})

export const stage = style({
  display: 'flex',
  flex: 1,
  alignItems: 'center',
  justifyContent: 'center',
  minWidth: 0,
  minHeight: 0,
  overflow: 'auto',
  borderRadius: vars.radius.lg,
  background: vars.color.surface.container
})

export const videoContainer = style({
  display: 'flex',
  alignItems: 'center',
  justifyContent: 'center',
  width: '100%',
  height: '100%',
  overflow: 'hidden'
})

export const video = style({
  maxWidth: '100%',
  maxHeight: '100%',
  borderRadius: vars.radius.sm,
  boxShadow: vars.elevation.lg
})

export const image = style({
  maxWidth: '100%',
  maxHeight: '100%',
  objectFit: 'contain'
})

export const text = style({
  width: '100%',
  height: '100%',
  margin: 0,
  padding: vars.space.lg,
  boxSizing: 'border-box',
  overflow: 'auto',
  whiteSpace: 'pre-wrap',
  overflowWrap: 'anywhere',
  color: 'inherit',
  ...typography('body'),
  lineHeight: '1.6'
})

export const card = style({
  display: 'flex',
  flexDirection: 'column',
  alignItems: 'center',
  gap: vars.space.sm,
  width: 'min(480px, 100%)',
  padding: vars.space.xl,
  boxSizing: 'border-box',
  borderRadius: vars.radius.lg,
  background: vars.color.surface.raised,
  color: 'inherit',
  textAlign: 'center'
})

export const cardTitle = style({
  margin: 0,
  ...typography('title')
})

export const cardReason = style({
  margin: 0,
  color: vars.color.text.secondary
})

export const cardDetail = style({
  margin: 0,
  color: vars.color.text.secondary,
  overflowWrap: 'anywhere',
  fontFamily: vars.font.mono,
  ...typography('bodySmall')
})

export const cardActions = style({
  display: 'flex',
  flexWrap: 'wrap',
  justifyContent: 'center',
  gap: vars.space.md,
  marginTop: vars.space.lg
})

export const archive = style({
  display: 'flex',
  flexDirection: 'column',
  gap: vars.space.sm,
  width: '100%',
  maxWidth: '720px',
  maxHeight: '100%',
  padding: vars.space.lg,
  boxSizing: 'border-box',
  overflow: 'auto',
  color: 'inherit'
})

export const archiveNote = style({
  margin: 0,
  ...typography('bodySmall')
})

export const archiveList = style({
  margin: 0,
  padding: 0,
  listStyle: 'none'
})

export const archiveRow = style({
  display: 'grid',
  gridTemplateColumns: 'auto 1fr auto auto',
  gap: vars.space.md,
  alignItems: 'center',
  width: '100%',
  padding: `${vars.space.sm} 0`,
  border: 0,
  borderBottom: `${vars.stroke.thin} solid ${vars.color.border.subtle}`,
  background: 'none',
  color: 'inherit',
  font: 'inherit',
  textAlign: 'start',
  selectors: {
    'button&': {
      cursor: 'pointer'
    },
    '&:hover': {
      background: vars.color.surface.fill
    },
    '&:focus-visible': focusOutline
  }
})

export const archiveRowUp = style({
  gridTemplateColumns: 'auto 1fr'
})

export const archiveName = style({
  minWidth: 0,
  overflow: 'hidden',
  textOverflow: 'ellipsis',
  whiteSpace: 'nowrap'
})

export const archiveSkipped = style({
  color: vars.color.danger.solid
})

export const crumbs = style({
  display: 'flex',
  flexWrap: 'wrap',
  alignItems: 'center',
  gap: vars.space.xs,
  ...typography('bodySmall')
})

export const crumb = style({
  maxWidth: '240px',
  overflow: 'hidden',
  padding: vars.space.xs,
  border: 0,
  borderRadius: vars.radius.xs,
  background: 'none',
  color: 'inherit',
  font: 'inherit',
  textOverflow: 'ellipsis',
  whiteSpace: 'nowrap',
  cursor: 'pointer',
  selectors: {
    '&:hover:not(:disabled)': {
      background: vars.color.surface.fill
    },
    '&:disabled': {
      cursor: 'default',
      opacity: 0.75
    },
    '&:focus-visible': focusOutline
  }
})

export const crumbSep = style({
  opacity: 0.5
})

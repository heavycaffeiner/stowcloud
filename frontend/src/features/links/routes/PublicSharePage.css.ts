import { style } from '@vanilla-extract/css'
import { media, typography, vars } from '@/shared/theme'

export const root = style({
  width: `min(100%, ${vars.layout.measure})`,
  minWidth: 0,
  overflowX: 'hidden',
  minHeight: '100dvh',
  marginInline: 'auto',
  padding: vars.layout.pagePad,
  '@media': {
    [media.compact]: {
      paddingBlock: vars.space.lg
    }
  }
})

export const header = style({
  display: 'flex',
  flexWrap: 'wrap',
  gap: `${vars.space.xs} ${vars.space.sm}`,
  marginBottom: vars.space.xxl,
  color: vars.color.text.secondary,
  ...typography('body'),
  '@media': {
    [media.compact]: {
      marginBottom: vars.space.xl
    }
  }
})

export const title = style({
  ...typography('heading'),
  margin: `0 0 ${vars.space.lg}`,
  overflowWrap: 'anywhere'
})

export const unlockTitle = style({
  ...typography('heading'),
  margin: 0,
  overflowWrap: 'anywhere'
})

export const status = style({
  margin: `0 0 ${vars.space.lg}`,
  overflowWrap: 'anywhere',
  color: vars.color.text.secondary
})

export const statusError = style({
  color: vars.color.danger.solid
})

export const state = style({
  display: 'flex',
  flexDirection: 'column',
  gap: vars.space.lg,
  minWidth: 0
})

export const unlock = style({
  display: 'flex',
  flexDirection: 'column',
  gap: vars.space.lg,
  minWidth: 0,
  maxWidth: '20rem',
  width: '100%'
})

export const stateError = style({
  padding: vars.space.lg,
  borderRadius: vars.radius.md,
  background: vars.color.danger.soft,
  color: vars.color.danger.onSoft
})

export const stateText = style({
  margin: 0,
  overflowWrap: 'anywhere'
})

export const unlockActions = style({
  alignSelf: 'flex-start'
})

export const drop = style({
  display: 'flex',
  flexDirection: 'column',
  alignItems: 'flex-start',
  gap: vars.space.sm,
  marginBottom: vars.space.xl
})

export const file = style({
  display: 'none'
})

export const list = style({
  display: 'flex',
  flexDirection: 'column',
  gap: 0,
  minWidth: 0,
  margin: `0 0 ${vars.space.xl}`,
  padding: 0,
  overflow: 'hidden',
  border: `${vars.stroke.thin} solid ${vars.color.border.subtle}`,
  borderRadius: vars.radius.md,
  background: 'transparent',
  listStyle: 'none'
})

export const row = style({
  display: 'flex',
  alignItems: 'center',
  gap: vars.space.md,
  minWidth: 0,
  minHeight: vars.density.row,
  padding: `${vars.space.sm} ${vars.space.lg}`,
  borderRadius: 0,
  background: 'transparent',
  borderBottom: `${vars.stroke.thin} solid ${vars.color.border.subtle}`,
  transition: `background-color ${vars.motion.short} ${vars.motion.easing}`,
  selectors: {
    '&:hover': {
      background: vars.color.surface.container
    },
    '&:last-child': {
      borderBottom: 'none'
    }
  },
  '@media': {
    [media.compact]: {
      gridTemplateColumns: 'minmax(0, 1fr) auto',
      paddingInline: vars.space.sm
    }
  }
})

export const rowEmpty = style({
  justifyContent: 'center',
  color: vars.color.text.secondary,
  textAlign: 'center'
})

export const icon = style({
  display: 'inline-flex',
  alignItems: 'center',
  justifyContent: 'center',
  flex: 'none',
  color: vars.color.text.icon
})

export const name = style({
  flex: 1,
  minWidth: 0,
  overflow: 'hidden',
  textOverflow: 'ellipsis',
  whiteSpace: 'nowrap',
  ...typography('body'),
  color: vars.color.text.primary,
  textAlign: 'start',
  selectors: {
    'button&': {
      border: 'none',
      background: 'transparent',
      cursor: 'pointer',
      padding: 0
    }
  }
})

export const size = style({
  flex: 'none',
  color: vars.color.text.secondary,
  ...typography('bodySmall'),
  whiteSpace: 'nowrap',
  fontVariantNumeric: 'tabular-nums',
  '@media': {
    [media.compact]: {
      maxWidth: 'min(42vw, 12rem)'
    }
  }
})

export const action = style({
  flex: 'none'
})

export const crumbs = style({
  marginBottom: vars.space.lg,
  minWidth: 0
})

export const crumbList = style({
  display: 'flex',
  flexWrap: 'wrap',
  alignItems: 'center',
  gap: vars.space.xs,
  minWidth: 0,
  maxWidth: '100%',
  margin: 0,
  padding: 0,
  listStyle: 'none'
})

export const crumbItem = style({
  display: 'flex',
  alignItems: 'center',
  gap: vars.space.xs,
  minWidth: 0,
  maxWidth: '100%'
})

export const crumb = style({
  minWidth: 0,
  minHeight: vars.density.controlDesktop,
  maxWidth: '100%',
  padding: `${vars.space.xs} ${vars.space.sm}`,
  overflow: 'hidden',
  border: 'none',
  borderRadius: vars.radius.sm,
  background: 'none',
  color: vars.color.accent.solid,
  font: 'inherit',
  textAlign: 'start',
  textOverflow: 'ellipsis',
  whiteSpace: 'nowrap',
  cursor: 'pointer',
  selectors: {
    '&:hover': {
      background: vars.color.surface.raised
    }
  },
  '@media': {
    [media.compact]: {
      minHeight: vars.density.controlCompact
    }
  }
})

export const folder = style({
  minWidth: 0,
  minHeight: vars.density.controlDesktop,
  maxWidth: '100%',
  padding: `${vars.space.xs} ${vars.space.sm}`,
  overflow: 'hidden',
  border: 'none',
  borderRadius: vars.radius.sm,
  background: 'none',
  color: vars.color.accent.solid,
  font: 'inherit',
  textAlign: 'start',
  textOverflow: 'ellipsis',
  whiteSpace: 'nowrap',
  cursor: 'pointer',
  selectors: {
    '&:hover': {
      background: vars.color.surface.raised
    }
  },
  '@media': {
    [media.compact]: {
      minHeight: vars.density.controlCompact
    }
  }
})

export const crumbSep = style({
  color: vars.color.text.secondary
})

export const crumbCurrent = style({
  minWidth: 0,
  maxWidth: '100%',
  overflowWrap: 'anywhere',
  fontWeight: vars.typography.label.weight
})

export const stateAction = style({
  alignSelf: 'flex-start'
})

export const rowActions = style({
  '@media': {
    [media.compact]: {
      gridColumn: '1 / -1',
      justifySelf: 'end'
    }
  }
})

export const rowAction = style({
  '@media': {
    [media.compact]: {
      maxWidth: '100%'
    }
  }
})

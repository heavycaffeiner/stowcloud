import { style } from '@vanilla-extract/css'
import { media, scaleUp, typography, vars } from '@/shared/theme'

export const page = style({
  minHeight: '100dvh',
  minWidth: 0,
  overflowY: 'auto',
  display: 'flex',
  alignItems: 'center',
  justifyContent: 'center',
  padding: vars.layout.pagePad,
  background: `radial-gradient(circle at 20% 15%, color-mix(in srgb, ${vars.color.accent.solid} 17%, transparent), transparent 24rem), radial-gradient(circle at 85% 80%, color-mix(in srgb, ${vars.color.neutral.solid} 12%, transparent), transparent 28rem), ${vars.color.surface.page}`,
  '@media': {
    [media.compact]: {
      alignItems: 'flex-start',
      paddingBlock: vars.space.lg
    }
  }
})

export const card = style({
  width: 'min(100%, 42rem)',
  minWidth: 0,
  display: 'flex',
  flexDirection: 'column',
  gap: vars.space.lg,
  padding: vars.space.xl,
  borderRadius: vars.radius.xl,
  background: vars.color.surface.raised,
  border: `${vars.stroke.thin} solid ${vars.color.border.subtle}`,
  boxShadow: vars.elevation.sm,
  animation: `${scaleUp} ${vars.motion.medium} ${vars.motion.easing}`,
  '@media': {
    [media.compact]: {
      gap: vars.space.md,
      padding: `${vars.space.xl} ${vars.space.lg}`
    }
  }
})

export const login = style({
  width: `min(100%, ${vars.layout.form})`,
  marginInline: 'auto',
  borderRadius: vars.radius.lg,
  boxShadow: 'none'
})

export const title = style({
  minWidth: 0,
  margin: 0,
  overflowWrap: 'anywhere',
  ...typography('heading'),
  textAlign: 'center'
})

export const subtitle = style({
  margin: 0,
  color: vars.color.text.secondary,
  overflowWrap: 'anywhere',
  textAlign: 'center'
})

export const licence = style({
  margin: 0,
  color: vars.color.text.secondary,
  overflowWrap: 'anywhere',
  textAlign: 'center'
})

export const hint = style({
  margin: 0,
  color: vars.color.text.secondary,
  overflowWrap: 'anywhere'
})

export const error = style({
  margin: 0,
  padding: `${vars.space.md} ${vars.space.lg}`,
  borderRadius: vars.radius.sm,
  overflowWrap: 'anywhere',
  background: vars.color.danger.soft,
  color: vars.color.danger.onSoft
})

export const success = style({
  margin: 0,
  padding: `${vars.space.md} ${vars.space.lg}`,
  borderRadius: vars.radius.sm,
  overflowWrap: 'anywhere',
  background: vars.color.selection.bg,
  color: vars.color.selection.fg
})

export const warning = style({
  margin: 0,
  padding: `${vars.space.md} ${vars.space.lg}`,
  borderRadius: vars.radius.sm,
  overflowWrap: 'anywhere',
  display: 'flex',
  flexDirection: 'column',
  gap: vars.space.sm,
  background: vars.color.highlight.soft,
  color: vars.color.highlight.onSoft
})

export const warningText = style({
  margin: 0
})

export const actions = style({
  display: 'flex',
  alignItems: 'center',
  justifyContent: 'flex-end',
  flexWrap: 'wrap',
  gap: vars.space.sm,
  marginTop: vars.space.sm,
  width: '100%',
  '@media': {
    [media.compact]: {
      flexDirection: 'column',
      alignItems: 'stretch'
    }
  }
})

export const divider = style({
  display: 'flex',
  alignItems: 'center',
  gap: vars.space.md,
  color: vars.color.text.secondary,
  selectors: {
    '&::before': {
      content: "''",
      flex: 1,
      borderTop: `${vars.stroke.thin} solid ${vars.color.text.secondary}`
    },
    '&::after': {
      content: "''",
      flex: 1,
      borderTop: `${vars.stroke.thin} solid ${vars.color.text.secondary}`
    }
  }
})

export const setupLink = style({
  paddingBlock: vars.space.xs,
  color: vars.color.accent.solid,
  textAlign: 'center',
  textDecoration: 'none',
  minHeight: vars.density.controlDesktop,
  display: 'flex',
  alignItems: 'center',
  justifyContent: 'center',
  overflowWrap: 'anywhere',
  selectors: {
    '&:hover': {
      textDecoration: 'underline'
    }
  },
  '@media': {
    [media.compact]: {
      minHeight: vars.density.controlCompact
    }
  }
})

export const licenceLink = style({
  color: vars.color.accent.solid
})

export const steps = style({
  display: 'flex',
  gap: vars.space.sm,
  flexWrap: 'wrap',
  margin: 0,
  padding: `${vars.space.sm} 0`,
  listStyle: 'none',
  color: vars.color.text.secondary
})

export const step = style({
  padding: `${vars.space.sm} ${vars.space.md}`,
  borderRadius: vars.radius.sm,
  border: `${vars.stroke.thin} solid ${vars.color.border.subtle}`
})

export const stepActive = style({
  color: vars.color.accent.solid,
  borderColor: vars.color.accent.solid,
  fontWeight: vars.font.weight.bold
})

export const pathRow = style({
  display: 'flex',
  gap: vars.space.sm,
  alignItems: 'center',
  '@media': {
    [media.compact]: {
      alignItems: 'stretch',
      flexDirection: 'column'
    }
  }
})

export const pathField = style({
  flex: '1 1 auto',
  minWidth: 0
})

export const pathButton = style({
  flex: 'none',
  '@media': {
    [media.compact]: {
      width: '100%'
    }
  }
})

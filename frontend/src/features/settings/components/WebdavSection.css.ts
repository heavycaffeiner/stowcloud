import { style } from '@vanilla-extract/css'
import { media, typography, vars } from '@/shared/theme'

export const tokenRow = style({
  display: 'flex',
  alignItems: 'center',
  gap: vars.space.sm,
  minWidth: 0,
  '@media': {
    [media.compact]: {
      alignItems: 'stretch'
    }
  }
})

export const root = style({
  display: 'flex',
  flexDirection: 'column',
  gap: vars.space.lg,
  minWidth: 0
})

export const os = style({
  selectors: {
    [`${root} section&`]: {
      display: 'flex',
      flexDirection: 'column',
      gap: vars.space.md,
      padding: `${vars.space.lg} 0 0`,
      border: 0,
      borderTop: `${vars.stroke.thin} solid ${vars.color.border.subtle}`,
      borderRadius: 0,
      background: 'transparent',
      boxShadow: 'none'
    }
  }
})

export const heading = style({
  margin: 0,
  ...typography('title'),
  color: vars.color.accent.solid,
  fontWeight: vars.font.weight.bold
})

export const subheading = style({
  margin: `${vars.space.md} 0 ${vars.space.xs}`,
  ...typography('titleSmall')
})

export const steps = style({
  margin: 0,
  paddingInlineStart: vars.space.xl,
  display: 'flex',
  flexDirection: 'column',
  gap: vars.space.xs,
  color: vars.color.text.secondary,
  ...typography('body')
})

export const tokenBlock = style({
  maxWidth: '100%',
  margin: 0,
  whiteSpace: 'pre-wrap',
  overflowWrap: 'anywhere'
})

export const credentials = style({
  margin: 0,
  color: vars.color.text.secondary,
  overflowWrap: 'anywhere'
})

export const nfcNote = style({
  margin: 0,
  color: vars.color.text.secondary,
  overflowWrap: 'anywhere'
})

export const announce = style({
  margin: 0,
  color: vars.color.text.secondary,
  overflowWrap: 'anywhere'
})

export const label = style({
  display: 'block',
  marginBottom: vars.space.xs,
  color: vars.color.text.secondary
})

export const token = style({
  flex: 1,
  minWidth: 0,
  display: 'inline-flex',
  alignItems: 'center',
  minHeight: vars.density.controlDesktop,
  margin: 0,
  padding: `${vars.space.sm} ${vars.space.md}`,
  boxSizing: 'border-box',
  fontFamily: vars.font.mono,
  ...typography('body'),
  borderRadius: vars.radius.xs,
  background: vars.color.surface.fill,
  color: vars.color.text.primary,
  overflowWrap: 'anywhere'
})

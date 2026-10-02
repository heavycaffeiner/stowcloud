import { style } from '@vanilla-extract/css'
import { media, typography, vars } from '@/shared/theme'

export const root = style({
  display: 'flex',
  flexDirection: 'column',
  gap: vars.space.lg,
  padding: `${vars.space.xl} 0`,
  minWidth: 0,
  border: 0,
  borderBottom: `${vars.stroke.thin} solid ${vars.color.border.subtle}`,
  borderRadius: 0,
  background: 'transparent',
  color: vars.color.text.primary,
  boxShadow: 'none',
  selectors: {
    '&:last-child': {
      borderBottom: 0
    }
  },
  '@media': {
    [media.compact]: {
      padding: `${vars.space.lg} 0`,
      gap: vars.space.md
    }
  },
  scrollMarginTop: vars.space.xl,
  marginBottom: 0
})

export const head = style({
  display: 'flex',
  alignItems: 'flex-start',
  gap: vars.space.lg,
  minWidth: 0,
  marginBottom: vars.space.xs,
  '@media': {
    [media.compact]: {
      gap: vars.space.md
    }
  }
})

export const title = style({
  margin: `0 0 ${vars.space.xxs}`,
  color: vars.color.text.primary,
  ...typography('titleLarge')
})

export const icon = style({
  display: 'flex',
  flex: 'none',
  alignItems: 'center',
  justifyContent: 'center',
  inlineSize: vars.density.controlDesktop,
  blockSize: vars.density.controlDesktop,
  borderRadius: vars.radius.sm,
  background: vars.color.surface.fill,
  color: vars.color.accent.solid
})

export const meta = style({
  flex: '1 1 auto',
  minWidth: 0,
  overflowWrap: 'anywhere'
})

export const subtitle = style({
  margin: `${vars.space.xs} 0 0`,
  color: vars.color.text.secondary,
  ...typography('bodySmall'),
  overflowWrap: 'anywhere'
})

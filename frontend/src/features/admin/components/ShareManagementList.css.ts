import { style } from '@vanilla-extract/css'
import { trailingJustify, trailingWidth } from '@/shared/ui/ListItem/ListItem.css'
import { media, typography, vars } from '@/shared/theme'

export const empty = style({
  display: 'flex',
  flexDirection: 'column',
  alignItems: 'center',
  gap: vars.space.sm,
  padding: `${vars.space.xxl} ${vars.space.lg}`,
  textAlign: 'center',
  border: `${vars.stroke.thin} dashed ${vars.color.border.subtle}`,
  borderRadius: vars.radius.md,
  background: 'transparent',
  color: vars.color.text.secondary
})

export const emptyText = style({
  margin: 0
})

export const list = style({
  listStyle: 'none',
  margin: 0,
  padding: 0,
  minWidth: 0,
  overflow: 'hidden',
  border: `${vars.stroke.thin} solid ${vars.color.border.subtle}`,
  borderRadius: vars.radius.md,
  background: 'transparent',
  display: 'flex',
  flexDirection: 'column',
  gap: 0
})

export const item = style({
  minWidth: 0,
  background: 'transparent',
  borderRadius: 0,
  display: 'flex',
  alignItems: 'center',
  justifyContent: 'space-between',
  gap: vars.space.lg,
  transition: `background-color ${vars.motion.short} ${vars.motion.easing}`,
  padding: 0,
  selectors: {
    '&:hover': {
      background: vars.color.surface.container
    },
    '& + &': {
      borderTop: `${vars.stroke.thin} solid ${vars.color.border.subtle}`
    }
  }
})

export const row = style({
  flex: 1,
  minWidth: 0,
  '@media': {
    [media.compact]: {
      vars: {
        [trailingWidth]: '100%',
        [trailingJustify]: 'flex-start'
      }
    }
  }
})

export const enc = style({
  display: 'flex',
  alignItems: 'center',
  flexWrap: 'wrap',
  gap: vars.space.sm,
  minWidth: 0
})

export const encNote = style({
  display: 'flex',
  alignItems: 'center',
  flexWrap: 'wrap',
  gap: vars.space.sm,
  minWidth: 0
})

export const encSaltRow = style({
  display: 'flex',
  alignItems: 'center',
  flexWrap: 'wrap',
  gap: vars.space.sm,
  minWidth: 0,
  marginTop: vars.space.xs
})

export const encSalt = style({
  minWidth: 0,
  maxWidth: '100%',
  overflowWrap: 'anywhere',
  fontFamily: vars.font.mono,
  ...typography('bodySmall'),
  background: vars.color.surface.raised,
  padding: `${vars.space.xxs} ${vars.space.xs}`,
  borderRadius: vars.radius.xs
})

export const trash = style({
  display: 'inline-flex',
  alignItems: 'center',
  gap: vars.space.sm,
  minHeight: vars.density.controlDesktop
})

export const trashLabel = style({
  ...typography('body'),
  color: vars.color.text.secondary,
  whiteSpace: 'nowrap'
})

export const encSaltLabel = style({
  ...typography('bodySmall'),
  color: vars.color.text.secondary
})

export const shareBackend = style({
  maxWidth: '100%',
  marginInlineStart: vars.space.sm,
  paddingInline: vars.space.sm,
  borderRadius: vars.radius.xs,
  background: vars.color.surface.fill,
  color: vars.color.text.secondary,
  ...typography('caption'),
  overflowWrap: 'anywhere'
})

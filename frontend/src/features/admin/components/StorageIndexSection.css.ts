import { style } from '@vanilla-extract/css'
import { media, typography, vars } from '@/shared/theme'

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
  padding: `${vars.space.md} ${vars.space.lg}`,
  display: 'flex',
  alignItems: 'center',
  justifyContent: 'space-between',
  gap: vars.space.lg,
  transition: `background-color ${vars.motion.short} ${vars.motion.easing}`,
  selectors: {
    '&:hover': {
      background: vars.color.surface.container
    },
    '& + &': {
      borderTop: `${vars.stroke.thin} solid ${vars.color.border.subtle}`
    }
  },
  '@media': {
    [media.compact]: {
      alignItems: 'flex-start',
      flexDirection: 'column',
      gap: vars.space.sm
    }
  }
})

export const value = style({
  flexShrink: 0,
  color: vars.color.text.secondary,
  ...typography('body'),
  fontWeight: vars.font.weight.medium,
  textAlign: 'end',
  overflowWrap: 'anywhere',
  '@media': {
    [media.compact]: {
      width: '100%',
      textAlign: 'start'
    }
  }
})

export const itemLabel = style({
  display: 'flex',
  alignItems: 'center',
  gap: vars.space.sm,
  minWidth: 0,
  fontWeight: vars.font.weight.medium,
  color: vars.color.text.primary
})

export const itemName = style({
  minWidth: 0,
  overflowWrap: 'anywhere'
})

export const statusRow = style({
  display: 'flex',
  alignItems: 'center',
  flexWrap: 'wrap',
  gap: vars.space.md
})

export const indexCost = style({
  margin: 0,
  padding: vars.space.lg,
  border: `${vars.stroke.thin} solid ${vars.color.border.subtle}`,
  borderRadius: vars.radius.md,
  background: 'transparent',
  display: 'flex',
  flexDirection: 'column',
  gap: vars.space.md
})

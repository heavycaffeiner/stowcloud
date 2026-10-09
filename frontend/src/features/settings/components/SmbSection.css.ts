import { style } from '@vanilla-extract/css'
import { media, typography, vars } from '@/shared/theme'

export const actions = style({
  display: 'flex',
  flexWrap: 'wrap',
  alignItems: 'center',
  justifyContent: 'flex-start',
  gap: vars.space.sm,
  '@media': {
    [media.compact]: {
      alignItems: 'stretch'
    }
  }
})

export const state = style({
  margin: 0,
  padding: 0,
  background: 'transparent',
  color: vars.color.text.secondary,
  ...typography('body'),
  overflowWrap: 'anywhere'
})

export const root = style({
  display: 'flex',
  flexDirection: 'column',
  gap: vars.space.lg,
  minWidth: 0
})

export const folder = style({
  display: 'flex',
  flexDirection: 'column',
  gap: vars.space.sm,
  padding: vars.space.md,
  borderRadius: vars.radius.md,
  border: `${vars.stroke.thin} solid ${vars.color.border.subtle}`
})

export const folderHeading = style({
  display: 'flex',
  flexWrap: 'wrap',
  alignItems: 'center',
  gap: vars.space.sm
})

export const address = style({
  display: 'flex',
  flexWrap: 'wrap',
  alignItems: 'center',
  gap: vars.space.sm,
  minWidth: 0,
  ...typography('bodySmall')
})

export const addressValue = style({
  flex: 1,
  minWidth: 0,
  overflowWrap: 'anywhere',
  userSelect: 'all'
})

export const note = style({
  margin: 0,
  color: vars.color.text.secondary,
  overflowWrap: 'anywhere'
})

export const announce = style({
  margin: 0,
  color: vars.color.text.secondary,
  overflowWrap: 'anywhere'
})

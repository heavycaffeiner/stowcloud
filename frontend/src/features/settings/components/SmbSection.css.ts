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

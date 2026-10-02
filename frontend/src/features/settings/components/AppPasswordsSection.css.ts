import { style } from '@vanilla-extract/css'
import { media, vars } from '@/shared/theme'

export const itemActions = style({
  display: 'flex',
  flexWrap: 'wrap',
  alignItems: 'center',
  justifyContent: 'flex-start',
  gap: vars.space.sm,
  '@media': {
    [media.compact]: {
      alignSelf: 'stretch',
      alignItems: 'stretch'
    }
  }
})

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

export const name = style({
  minWidth: 0,
  overflowWrap: 'anywhere'
})

import { style } from '@vanilla-extract/css'
import { media, vars } from '@/shared/theme'

export const actions = style({
  display: 'flex',
  flexWrap: 'wrap',
  alignItems: 'center',
  justifyContent: 'flex-start',
  gap: vars.space.sm,
  marginTop: vars.space.sm,
  '@media': {
    [media.compact]: {
      alignItems: 'stretch'
    }
  }
})

export const success = style({
  margin: 0,
  color: vars.color.accent.solid,
  overflowWrap: 'anywhere'
})

export const root = style({
  display: 'flex',
  flexDirection: 'column',
  gap: vars.space.lg,
  width: '100%',
  maxWidth: vars.layout.form
})

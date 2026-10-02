import { style } from '@vanilla-extract/css'
import { media, vars } from '@/shared/theme'

export const codes = style({
  display: 'grid',
  gridTemplateColumns: 'repeat(2, minmax(0, 1fr))',
  gap: vars.space.sm,
  listStyle: 'none',
  margin: 0,
  padding: 0,
  '@media': {
    [media.compact]: {
      gridTemplateColumns: '1fr'
    }
  }
})

export const recoveryCount = style({
  margin: 0,
  color: vars.color.text.secondary,
  overflowWrap: 'anywhere'
})

export const recoveryCountLow = style({
  color: vars.color.danger.solid,
  fontWeight: vars.font.weight.medium
})

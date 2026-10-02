import { style } from '@vanilla-extract/css'
import { media, typography, vars } from '@/shared/theme'

export const coverage = style({
  margin: `calc(-1 * ${vars.space.sm}) 0 ${vars.space.lg}`,
  color: vars.color.text.secondary,
  overflowWrap: 'anywhere',
  ...typography('bodySmall')
})

export const meta = style({
  flexShrink: 0,
  color: vars.color.text.secondary,
  whiteSpace: 'nowrap',
  ...typography('bodySmall'),
  '@media': { [media.compact]: { display: 'none' } }
})

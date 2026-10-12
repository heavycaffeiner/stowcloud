import { style } from '@vanilla-extract/css'
import { media, typography, vars } from '@/shared/theme'

export const meta = style({
  flexShrink: 0,
  color: vars.color.text.secondary,
  whiteSpace: 'nowrap',
  ...typography('bodySmall'),
  '@media': { [media.compact]: { display: 'none' } }
})

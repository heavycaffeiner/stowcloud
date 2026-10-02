import { style } from '@vanilla-extract/css'
import { media, vars } from '@/shared/theme'

export const linear = style({
  vars: { '--progress-size': vars.space.xs, '--progress-radius': vars.radius.full },
  inlineSize: '100%',
  background: vars.color.surface.fill
})

export const section = style({
  '@media': { [media.reducedMotion]: { animation: 'none' } }
})

import { style } from '@vanilla-extract/css'
import { typography, vars } from '@/shared/theme'

/** The heading over the folders or the files. */
export const group = style({
  margin: 0,
  paddingBlock: `${vars.space.lg} ${vars.space.sm}`,
  paddingInline: vars.layout.contentPad,
  color: vars.color.text.secondary,
  ...typography('titleSmall')
})

export const window = style({
  paddingInline: vars.layout.contentPad
})

export const row = style({
  display: 'flex',
  alignItems: 'stretch'
})

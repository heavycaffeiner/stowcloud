import { style } from '@vanilla-extract/css'
import { typography, vars } from '@/shared/theme'

// Pulled up against the password field above it, inside a form whose gap is wider.
export const root = style({
  display: 'flex',
  alignItems: 'center',
  gap: vars.space.sm,
  marginTop: `calc(-1 * ${vars.space.sm})`
})

export const bar = style({
  flex: 1
})

export const label = style({
  flex: 'none',
  minInlineSize: vars.density.row,
  color: vars.color.text.secondary,
  ...typography('bodySmall')
})

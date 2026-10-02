import { style } from '@vanilla-extract/css'
import { media, typography, vars } from '@/shared/theme'

export const formRow = style({
  display: 'flex',
  alignItems: 'center',
  gap: vars.space.sm,
  minWidth: 0,
  '@media': {
    [media.compact]: {
      alignItems: 'stretch',
      flexDirection: 'column'
    }
  }
})

export const chips = style({
  display: 'flex',
  flexWrap: 'wrap',
  gap: vars.space.sm,
  listStyle: 'none',
  margin: 0,
  padding: 0
})

// A member name that ends in its own remove button, which sets the height.
export const memberChip = style({
  display: 'inline-flex',
  alignItems: 'center',
  gap: vars.space.xxs,
  paddingInlineStart: vars.space.md,
  borderRadius: vars.radius.full,
  background: vars.color.surface.fill,
  color: vars.color.text.secondary,
  ...typography('caption'),
  whiteSpace: 'nowrap'
})

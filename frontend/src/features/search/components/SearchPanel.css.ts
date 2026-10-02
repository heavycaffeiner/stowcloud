import { style } from '@vanilla-extract/css'
import { media, typography, vars } from '@/shared/theme'

export const categoryPill = style({
  flex: 'none',
  '@media': { [media.compact]: { scrollSnapAlign: 'start' } }
})

export const root = style({
  display: 'flex',
  flexDirection: 'column',
  gap: vars.space.md,
  width: '100%',
  color: vars.color.text.primary
})

export const queryBar = style({
  display: 'flex',
  alignItems: 'center',
  gap: vars.space.sm
})

export const queryField = style({
  selectors: { [`${queryBar} &`]: { flex: 1, maxInlineSize: 'none' } }
})

// When the kinds do not fit beside the scope and sort, they take their own lines and those two wrap below.
export const filterBar = style({
  display: 'flex',
  flexWrap: 'wrap',
  alignItems: 'center',
  justifyContent: 'space-between',
  gap: vars.space.sm,
  padding: `${vars.space.xxs} 0 ${vars.space.xs}`,
  overflow: 'visible',
  scrollbarWidth: 'none'
})

// A phone keeps the kinds on one full-width line that scrolls sideways, with the scope and sort below.
export const categories = style({
  display: 'flex',
  flex: '1 1 auto',
  flexWrap: 'wrap',
  alignItems: 'center',
  gap: vars.space.xs,
  '@media': {
    [media.compact]: {
      flex: 'none',
      flexWrap: 'nowrap',
      overflowX: 'auto',
      scrollbarWidth: 'none',
      width: '100%',
      paddingBottom: vars.space.xxs,
      overscrollBehaviorInline: 'contain',
      scrollSnapType: 'x proximity'
    }
  }
})

export const scope = style({
  flex: 'none'
})

export const sort = style({
  flex: 'none'
})

export const statusBar = style({
  display: 'flex',
  alignItems: 'center',
  justifyContent: 'space-between',
  minHeight: vars.space.xl,
  padding: `0 ${vars.space.xxs}`,
  ...typography('bodySmall'),
  color: vars.color.text.secondary,
  '@media': {
    [media.compact]: {
      alignItems: 'stretch',
      flexDirection: 'column',
      gap: vars.space.sm
    }
  }
})

export const statusInfo = style({
  display: 'inline-flex',
  alignItems: 'center',
  gap: vars.space.xs
})

export const progress = style({
  display: 'inline-flex',
  alignItems: 'center'
})

export const statusActions = style({
  display: 'flex',
  alignItems: 'center',
  gap: vars.space.sm,
  '@media': {
    [media.compact]: {
      justifyContent: 'flex-end'
    }
  }
})

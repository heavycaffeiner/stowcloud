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

export const filterBar = style({
  display: 'flex',
  alignItems: 'center',
  justifyContent: 'space-between',
  gap: vars.space.sm,
  padding: `${vars.space.xxs} 0 ${vars.space.xs}`,
  overflow: 'visible',
  scrollbarWidth: 'none',
  '@media': {
    [media.compact]: {
      alignItems: 'stretch',
      flexDirection: 'column',
      gap: vars.space.sm,
      overflow: 'visible'
    }
  }
})

export const categories = style({
  display: 'flex',
  alignItems: 'center',
  gap: vars.space.xs,
  flex: '1 1 auto',
  overflowX: 'auto',
  scrollbarWidth: 'none',
  '@media': {
    [media.compact]: {
      width: '100%',
      flex: 'none',
      paddingBottom: vars.space.xxs,
      overscrollBehaviorInline: 'contain',
      scrollSnapType: 'x proximity'
    }
  }
})

export const scope = style({
  '@media': { [media.compact]: { alignSelf: 'flex-start' } }
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

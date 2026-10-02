import { style } from '@vanilla-extract/css'
import { media, typography, vars } from '@/shared/theme'

export const root = style({
  display: 'flex',
  alignItems: 'center',
  justifyContent: 'center',
  minHeight: '100dvh',
  minWidth: 0,
  overflowY: 'auto',
  padding: vars.layout.pagePad,
  '@media': {
    [media.compact]: {
      alignItems: 'flex-start',
      paddingBlock: vars.space.lg
    }
  }
})

export const card = style({
  display: 'flex',
  flexDirection: 'column',
  gap: vars.space.lg,
  width: 'min(640px, 100%)',
  minWidth: 0,
  padding: vars.space.xl,
  borderRadius: vars.radius.xl,
  background: vars.color.surface.container,
  boxShadow: vars.elevation.lg,
  '@media': {
    [media.compact]: {
      gap: vars.space.md,
      padding: `${vars.space.xl} ${vars.space.lg}`
    }
  }
})

export const title = style({
  margin: 0,
  overflowWrap: 'anywhere',
  ...typography('heading')
})

export const hint = style({
  margin: 0,
  color: vars.color.text.secondary,
  overflowWrap: 'anywhere'
})

export const form = style({
  display: 'flex',
  flexDirection: 'column',
  gap: vars.space.md
})

export const actions = style({
  display: 'flex',
  gap: vars.space.sm,
  flexWrap: 'wrap',
  alignItems: 'center',
  '@media': {
    [media.compact]: {
      flexDirection: 'column',
      alignItems: 'stretch'
    }
  }
})

export const facts = style({
  display: 'grid',
  gridTemplateColumns: 'auto 1fr',
  gap: `${vars.space.xs} ${vars.space.lg}`,
  margin: 0,
  '@media': {
    [media.compact]: {
      gridTemplateColumns: 'minmax(0, 1fr)',
      gap: vars.space.xxs
    }
  }
})

export const factValue = style({
  minWidth: 0,
  margin: 0,
  overflowWrap: 'anywhere'
})

export const factLabel = style({
  color: vars.color.text.secondary,
  '@media': {
    [media.compact]: {
      selectors: {
        [`${factValue} + &`]: {
          marginTop: vars.space.sm
        }
      }
    }
  }
})

export const warning = style({
  margin: 0,
  padding: `${vars.space.md} ${vars.space.lg}`,
  borderRadius: vars.radius.sm,
  overflowWrap: 'anywhere',
  background: vars.color.highlight.soft,
  color: vars.color.highlight.onSoft
})

export const error = style({
  margin: 0,
  padding: `${vars.space.md} ${vars.space.lg}`,
  borderRadius: vars.radius.sm,
  overflowWrap: 'anywhere',
  background: vars.color.danger.soft,
  color: vars.color.danger.onSoft
})

export const ok = style({
  margin: 0,
  color: vars.color.accent.solid,
  overflowWrap: 'anywhere'
})

export const action = style({
  '@media': {
    [media.compact]: {
      flex: '1 1 12rem'
    }
  }
})

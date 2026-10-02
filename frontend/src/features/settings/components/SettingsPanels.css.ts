import { style } from '@vanilla-extract/css'
import { media, typography, vars } from '@/shared/theme'

export const pageGrid = style({
  display: 'grid',
  gap: 0,
  minWidth: 0
})

export const cardIcon = style({
  flex: 'none',
  display: 'flex',
  alignItems: 'center',
  justifyContent: 'center',
  width: vars.density.controlDesktop,
  height: vars.density.controlDesktop,
  borderRadius: vars.radius.sm,
  background: vars.color.surface.fill,
  color: vars.color.accent.solid
})

export const avatar = style({
  flex: 'none',
  display: 'flex',
  alignItems: 'center',
  justifyContent: 'center',
  width: vars.density.controlDesktop,
  height: vars.density.controlDesktop,
  borderRadius: vars.radius.full,
  background: vars.color.accent.solid,
  color: vars.color.accent.onSolid,
  ...typography('title'),
  fontWeight: vars.font.weight.bold
})

export const accountName = style({
  margin: 0,
  overflowWrap: 'anywhere',
  ...typography('bodyLarge'),
  fontWeight: vars.font.weight.medium
})

export const username = style({
  margin: 0,
  overflowWrap: 'anywhere',
  color: vars.color.text.secondary,
  ...typography('bodySmall')
})

export const cardHint = style({
  overflowWrap: 'anywhere',
  maxWidth: vars.layout.measure,
  margin: 0,
  color: vars.color.text.secondary,
  ...typography('bodySmall')
})

export const row = style({
  display: 'flex',
  alignItems: 'center',
  flexWrap: 'wrap',
  gap: vars.space.md,
  minWidth: 0
})

export const rowSegmented = style({
  display: 'block',
  maxWidth: '100%',
  overflowX: 'auto',
  paddingBlock: vars.space.xxs,
  scrollbarWidth: 'none',
  '@media': {
    [media.compact]: {
      maxWidth: `calc(100vw - (2 * ${vars.layout.contentPad}))`
    }
  }
})

export const segmented = style({
  width: 'max-content',
  minWidth: 'max-content',
  maxWidth: '100%'
})

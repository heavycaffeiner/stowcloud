import { createVar, fallbackVar, style } from '@vanilla-extract/css'
import { media, typography, vars } from '@/shared/theme'

/** How the trailing part lines up its contents. Defaults to the end edge. */
export const trailingJustify = createVar()

/** The trailing part's width. Defaults to its content. */
export const trailingWidth = createVar()

export const root = style({
  display: 'flex',
  flexWrap: 'wrap',
  alignItems: 'center',
  gap: vars.space.lg,
  minBlockSize: vars.density.row,
  paddingBlock: vars.space.sm,
  paddingInline: vars.space.lg,
  color: vars.color.text.primary,
  '@media': { [media.compact]: { gap: vars.space.md, paddingInline: vars.space.md } }
})

export const leading = style({
  display: 'inline-flex',
  alignItems: 'center',
  justifyContent: 'center',
  flex: `0 0 ${vars.space.xxl}`,
  blockSize: vars.space.xxl
})

export const text = style({
  display: 'flex',
  flex: '1 0 12rem',
  flexDirection: 'column',
  minInlineSize: 0,
  '@media': { [media.compact]: { flexBasis: '10rem' } }
})

export const headline = style({
  display: 'flex',
  alignItems: 'center',
  flexWrap: 'wrap',
  gap: vars.space.sm,
  minInlineSize: 0,
  overflowWrap: 'anywhere',
  ...typography('bodyLarge')
})

export const supporting = style({
  ...typography('body'),
  color: vars.color.text.secondary,
  overflowWrap: 'anywhere'
})

export const trailing = style({
  display: 'inline-flex',
  flexWrap: 'wrap',
  alignItems: 'center',
  justifyContent: fallbackVar(trailingJustify, 'flex-end'),
  gap: vars.space.sm,
  marginInlineStart: 'auto',
  maxInlineSize: '100%',
  inlineSize: fallbackVar(trailingWidth, 'auto')
})

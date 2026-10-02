import { style } from '@vanilla-extract/css'
import { focusOutline, media, typography, vars } from '@/shared/theme'

export const root = style({
  blockSize: '100%',
  minInlineSize: 0,
  overflowX: 'hidden',
  overflowY: 'auto',
  wordBreak: 'keep-all'
})

export const inner = style({
  display: 'flex',
  flexDirection: 'column',
  inlineSize: 'min(100%, 53.75rem)',
  minInlineSize: 0,
  minBlockSize: '100%',
  marginInline: 'auto',
  padding: vars.layout.pagePad
})

export const header = style({
  display: 'flex',
  flex: 'none',
  alignItems: 'center',
  gap: vars.space.sm,
  minInlineSize: 0,
  marginBlockEnd: vars.space.lg,
  '@media': { [media.compact]: { marginBlockEnd: vars.space.md } }
})

export const title = style({
  flex: 1,
  minInlineSize: 0,
  margin: 0,
  overflowWrap: 'anywhere',
  ...typography('heading')
})

export const error = style({
  margin: `0 0 ${vars.space.lg}`,
  paddingBlock: vars.space.md,
  paddingInline: vars.space.lg,
  overflowWrap: 'anywhere',
  borderRadius: vars.radius.sm,
  background: vars.color.danger.soft,
  color: vars.color.danger.onSoft
})

export const state = style({
  display: 'grid',
  flex: '1 1 12rem',
  placeItems: 'center',
  minBlockSize: '8rem',
  margin: 0,
  padding: vars.space.xxl,
  color: vars.color.text.secondary,
  textAlign: 'center',
  '@media': { [media.compact]: { paddingBlock: vars.space.xl, paddingInline: vars.space.lg } }
})

export const list = style({
  display: 'flex',
  flexDirection: 'column',
  minInlineSize: 0,
  margin: 0,
  padding: 0,
  listStyle: 'none'
})

export const row = style({
  display: 'flex',
  alignItems: 'center',
  gap: vars.space.md,
  inlineSize: '100%',
  minBlockSize: vars.density.row,
  paddingBlock: vars.space.sm,
  paddingInline: vars.space.lg,
  border: 0,
  borderBlockEnd: `${vars.stroke.thin} solid ${vars.color.border.subtle}`,
  background: 'none',
  color: 'inherit',
  font: 'inherit',
  textAlign: 'start',
  cursor: 'pointer',
  selectors: {
    '&:hover': { background: vars.color.surface.container },
    // The list clips its sides, so the outline sits inside the row.
    '&:focus-visible': { ...focusOutline, outlineOffset: vars.focusRing.insetOffset }
  },
  '@media': { [media.compact]: { gap: vars.space.sm, paddingInline: vars.space.sm } }
})

export const icon = style({
  display: 'inline-flex',
  flex: 'none',
  color: vars.color.text.secondary
})

export const text = style({
  display: 'flex',
  flex: 1,
  flexDirection: 'column',
  minInlineSize: 0
})

export const name = style({
  overflow: 'hidden',
  textOverflow: 'ellipsis',
  whiteSpace: 'nowrap'
})

export const path = style({
  overflow: 'hidden',
  textOverflow: 'ellipsis',
  whiteSpace: 'nowrap',
  color: vars.color.text.secondary,
  ...typography('bodySmall')
})

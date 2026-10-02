import { style } from '@vanilla-extract/css'
import { focusOutline, typography, vars } from '@/shared/theme'

const safeBottom = 'env(safe-area-inset-bottom, 0px)'

export const root = style({
  position: 'fixed',
  zIndex: 40,
  insetBlockEnd: 0,
  insetInline: 0,
  blockSize: `calc(${vars.layout.navBar} + ${safeBottom})`,
  display: 'flex',
  alignItems: 'stretch',
  justifyContent: 'center',
  gap: vars.space.xs,
  paddingBlock: `${vars.space.xs} calc(${vars.space.xs} + ${safeBottom})`,
  paddingInline: `max(${vars.space.sm}, env(safe-area-inset-left, 0px)) max(${vars.space.sm}, env(safe-area-inset-right, 0px))`,
  borderBlockStart: `${vars.stroke.thin} solid ${vars.color.border.subtle}`,
  background: vars.color.surface.container,
  userSelect: 'none'
})

export const item = style({
  flex: '1 1 0',
  minInlineSize: 0,
  maxInlineSize: '7rem',
  minBlockSize: vars.density.row,
  display: 'flex',
  flexDirection: 'column',
  alignItems: 'center',
  justifyContent: 'center',
  gap: vars.space.xxs,
  padding: `${vars.space.xxs} ${vars.space.xxs} ${vars.space.xs}`,
  border: 0,
  borderRadius: vars.radius.lg,
  background: 'transparent',
  color: vars.color.text.secondary,
  font: 'inherit',
  ...typography('labelSmall'),
  cursor: 'pointer',
  transition: `color ${vars.motion.short} ${vars.motion.easing}`,
  selectors: { '&:focus-visible': focusOutline }
})

// The active destination also gets a filled indicator behind its icon, so it never relies on color alone.
export const itemActive = style({
  color: vars.color.selection.fg,
  fontWeight: vars.font.weight.bold
})

export const icon = style({
  display: 'grid',
  flex: 'none',
  placeItems: 'center',
  inlineSize: '4rem',
  blockSize: vars.space.xxl,
  borderRadius: vars.radius.lg,
  transition: `background-color ${vars.motion.short} ${vars.motion.easing}`,
  selectors: { [`${itemActive} &`]: { background: vars.color.selection.bg } }
})

export const label = style({
  inlineSize: '100%',
  minInlineSize: 0,
  overflow: 'hidden',
  textAlign: 'center',
  textOverflow: 'ellipsis',
  whiteSpace: 'nowrap'
})

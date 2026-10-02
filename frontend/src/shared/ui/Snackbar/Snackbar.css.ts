import { style } from '@vanilla-extract/css'
import { typography, vars } from '@/shared/theme'

export const root = style({
  vars: { '--notification-radius': vars.radius.xs },
  position: 'fixed',
  insetBlockEnd: `calc(${vars.space.lg} + env(safe-area-inset-bottom, 0px))`,
  insetInlineStart: '50%',
  translate: '-50% 0',
  zIndex: 400,
  inlineSize: 'max-content',
  minInlineSize: `min(18rem, calc(100vw - 2 * ${vars.space.lg}))`,
  maxInlineSize: `min(42rem, calc(100vw - 2 * ${vars.space.lg}))`,
  paddingInlineStart: vars.space.lg,
  background: vars.color.surface.inverse,
  boxShadow: vars.elevation.lg,
  selectors: { '&::before': { display: 'none' } }
})

export const description = style({
  ...typography('body'),
  color: vars.color.text.inverse,
  overflowWrap: 'anywhere'
})

export const closeButton = style({
  color: vars.color.text.inverse,
  selectors: {
    '&:hover': { background: `color-mix(in srgb, ${vars.color.text.inverse} 8%, transparent)` }
  }
})

import { style } from '@vanilla-extract/css'
import { typography, vars } from '@/shared/theme'

/** The zero-size point a context menu lines up with. */
export const anchor = style({
  position: 'fixed',
  inlineSize: 0,
  blockSize: 0,
  pointerEvents: 'none'
})

export const dropdown = style({
  minInlineSize: '12.5rem',
  maxInlineSize: `min(22.5rem, calc(100vw - ${vars.space.lg}))`,
  maxBlockSize: `calc(100dvh - ${vars.space.lg})`,
  overflowY: 'auto',
  padding: vars.space.sm,
  // A transparent border still draws in forced-colors mode, where the shadow does not.
  borderColor: 'transparent',
  borderRadius: vars.radius.md,
  background: vars.color.surface.overlay,
  boxShadow: vars.elevation.md
})

export const item = style({
  vars: { '--menu-item-hover': `color-mix(in srgb, ${vars.color.text.primary} 8%, transparent)` },
  minBlockSize: vars.density.control,
  paddingBlock: vars.space.xs,
  paddingInline: vars.space.md,
  borderRadius: vars.radius.sm,
  textAlign: 'start',
  ...typography('body')
})

export const label = style({
  minInlineSize: 0,
  overflowWrap: 'anywhere'
})

export const icon = style({
  color: vars.color.text.icon
})

export const indicator = style({
  inlineSize: 'auto',
  blockSize: 'auto',
  color: vars.color.accent.solid
})

export const sheetHandle = style({
  display: 'flex',
  justifyContent: 'center',
  paddingBlock: vars.space.sm,
  selectors: {
    '&::before': {
      content: "''",
      inlineSize: '2rem',
      blockSize: vars.space.xs,
      borderRadius: vars.radius.full,
      background: vars.color.border.subtle
    }
  }
})

export const sheetList = style({
  display: 'flex',
  flexDirection: 'column',
  gap: vars.space.xs,
  paddingInline: vars.space.md,
  paddingBlockEnd: vars.space.sm,
  overflowY: 'auto'
})

export const sheetItem = style({
  minBlockSize: vars.density.row
})

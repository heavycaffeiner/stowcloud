import { style } from '@vanilla-extract/css'
import { focusOutline, typography, vars } from '@/shared/theme'

export const root = style({
  display: 'flex',
  flex: 'none',
  alignItems: 'center',
  gap: vars.space.md,
  inlineSize: '100%',
  minBlockSize: '48px',
  padding: `${vars.space.sm} ${vars.space.lg}`,
  boxSizing: 'border-box',
  border: 'none',
  background: 'transparent',
  color: vars.color.text.primary,
  ...typography('body'),
  textAlign: 'start',
  cursor: 'pointer',
  selectors: { '&:hover': { background: vars.color.surface.container } }
})

export const label = style({
  color: vars.color.text.secondary
})

export const navigationRoot = style({
  position: 'relative'
})

export const navigationName = style({
  overflow: 'visible'
})

export const navigate = style({
  display: 'flex',
  flex: '1 1 auto',
  alignItems: 'center',
  gap: vars.space.sm,
  minInlineSize: 0,
  padding: 0,
  border: 0,
  background: 'transparent',
  color: 'inherit',
  font: 'inherit',
  textAlign: 'start',
  cursor: 'pointer',
  selectors: {
    '&::after': { content: "''", position: 'absolute', inset: 0 },
    '&:focus-visible': { outline: 'none' },
    '&:focus-visible::after': { ...focusOutline, outlineOffset: vars.focusRing.insetOffset }
  }
})

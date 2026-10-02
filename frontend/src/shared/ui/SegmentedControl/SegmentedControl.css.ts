import { style } from '@vanilla-extract/css'
import { focusOutline, typography, vars } from '@/shared/theme'

export const root = style({
  vars: {
    '--sc-radius': vars.radius.full,
    '--sc-padding': `${vars.space.sm} ${vars.space.lg}`,
    '--sc-color': vars.color.selection.bg
  },
  background: vars.color.surface.fill
})

// The raised chip marks the choice in both schemes, not its color alone.
export const indicator = style({
  boxShadow: vars.elevation.sm
})

export const input = style({})

export const label = style({
  ...typography('label'),
  color: vars.color.text.secondary,
  selectors: {
    '&[data-active]': { color: vars.color.selection.fg },
    // The group clips its overflow, so the ring sits inside the segment.
    [`${input}:focus-visible + &`]: { ...focusOutline, outlineOffset: vars.focusRing.insetOffset }
  }
})

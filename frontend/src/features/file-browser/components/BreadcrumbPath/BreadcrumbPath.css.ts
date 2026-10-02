import { style } from '@vanilla-extract/css'
import { focusRing, media, typography, vars } from '@/shared/theme'

export const root = style({
  display: 'flex',
  flex: '1 1 auto',
  alignItems: 'center',
  minWidth: 0,
  maxWidth: '100%',
  height: vars.density.control,
  ...typography('label')
})

export const list = style({
  display: 'flex',
  flex: '1 1 auto',
  alignItems: 'center',
  minWidth: 0,
  width: '100%',
  margin: 0,
  padding: 0,
  listStyle: 'none'
})

export const item = style({
  display: 'inline-flex',
  flex: '0 1 auto',
  alignItems: 'center',
  minWidth: 0,
  height: vars.density.control,
  whiteSpace: 'nowrap'
})

// Each place in the trail caps its share of the row, so a long name cannot push the current folder out.
export const itemRoot = style({
  maxWidth: 'min(144px, 22%)',
  '@media': { [media.compact]: { maxWidth: '30%' } }
})

export const itemAncestor = style({
  maxWidth: 'min(160px, 18%)'
})

export const itemParent = style({
  maxWidth: 'min(200px, 24%)'
})

export const itemCurrent = style({
  maxWidth: '360px',
  flex: '1 1 0'
})

export const itemEllipsis = style({
  flex: '0 0 auto'
})

const crumb = style({
  display: 'inline-flex',
  flex: '1 1 auto',
  alignItems: 'center',
  minWidth: 0,
  maxWidth: '100%',
  height: vars.density.control,
  boxSizing: 'border-box',
  paddingInline: vars.space.sm,
  borderRadius: vars.radius.sm,
  font: 'inherit',
  textAlign: 'start',
  '@media': { [media.compact]: { paddingInline: vars.space.xs } }
})

export const link = style([
  crumb,
  focusRing,
  {
    width: '100%',
    border: 0,
    background: 'transparent',
    color: vars.color.accent.solid,
    cursor: 'pointer',
    transition: `background-color ${vars.motion.short} ${vars.motion.easing}`,
    selectors: {
      '&:hover': { background: `color-mix(in srgb, ${vars.color.accent.solid} 8%, transparent)` },
      '&:active': { background: `color-mix(in srgb, ${vars.color.accent.solid} 14%, transparent)` }
    }
  }
])

export const current = style([crumb, { color: vars.color.text.primary }])

export const label = style({
  display: 'block',
  minWidth: 0,
  overflow: 'hidden',
  textOverflow: 'ellipsis',
  whiteSpace: 'nowrap',
  selectors: {
    [`${itemRoot} &`]: {
      color: vars.color.accent.solid,
      fontWeight: vars.font.weight.bold
    }
  }
})

export const sep = style({
  display: 'inline-flex',
  flex: 'none',
  alignItems: 'center',
  justifyContent: 'center',
  height: vars.density.control,
  paddingInline: vars.space.xxs,
  color: vars.color.text.secondary,
  font: 'inherit',
  opacity: 0.55,
  userSelect: 'none'
})

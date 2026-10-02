import { style } from '@vanilla-extract/css'
import { focusOutline, focusRing, media, typography, vars } from '@/shared/theme'
import { container } from '../file-view.css'
import { mobile } from '../FileItem/FileItem.css'

/** The column headings. They take the row's columns, so each heading sits over its cells. */
export const header = style({
  position: 'sticky',
  zIndex: 2,
  insetBlockStart: 0,
  display: 'flex',
  alignItems: 'center',
  paddingInline: vars.layout.contentPad,
  background: `color-mix(in srgb, ${vars.color.surface.container} 70%, ${vars.color.surface.page})`,
  color: vars.color.text.secondary,
  ...typography('label'),
  '@container': { [`${container} ${media.compact}`]: { paddingInline: vars.space.lg } },
  selectors: { [`${mobile} &`]: { paddingInline: vars.space.md } }
})

export const selectAll = style([
  focusRing,
  {
    display: 'inline-flex',
    alignItems: 'center',
    justifyContent: 'center',
    padding: 0,
    border: 0,
    borderRadius: vars.radius.xs,
    background: 'transparent',
    color: 'inherit',
    cursor: 'pointer'
  }
])

// The heading cells clip, so the focus outline is drawn inside the button.
export const sortButton = style({
  display: 'inline-flex',
  alignItems: 'center',
  gap: vars.space.xs,
  minInlineSize: 0,
  minBlockSize: vars.density.controlDesktop,
  paddingInline: vars.space.xs,
  border: 0,
  borderRadius: vars.radius.xs,
  background: 'transparent',
  color: 'inherit',
  font: 'inherit',
  cursor: 'pointer',
  transition: `background-color ${vars.motion.short} ${vars.motion.easing}, color ${vars.motion.short} ${vars.motion.easing}`,
  selectors: {
    '&:hover': {
      background: 'color-mix(in srgb, currentColor 10%, transparent)',
      color: vars.color.text.primary
    },
    '&:focus-visible': { ...focusOutline, outlineOffset: vars.focusRing.insetOffset }
  },
  '@media': { [media.reducedMotion]: { transition: 'none' } }
})

// The active key is told apart by the arrow and the weight as well as the color.
export const sortActive = style({
  color: vars.color.accent.solid,
  fontWeight: vars.typography.titleSmall.weight
})

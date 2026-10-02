import { createContainer, createVar, fallbackVar, style } from '@vanilla-extract/css'
import { focusOutline, typography, vars } from '@/shared/theme'

/** The view's own width, which decides the columns rather than the window's. */
export const container = createContainer()

const selectionBarSpace = createVar()

// The scrolling element of the grid and the list. It owns the scroll, so the virtual windows read their offsets
// from it rather than from the page. Scroll anchoring is off: moving the window would otherwise shift the offset
// the window was placed by, and the two would chase each other.
export const root = style({
  position: 'relative',
  flex: 1,
  alignSelf: 'stretch',
  minInlineSize: 0,
  minBlockSize: 0,
  overflow: 'auto',
  overflowAnchor: 'none',
  overscrollBehaviorY: 'contain',
  contain: 'content',
  containerType: 'inline-size',
  containerName: container,
  paddingBlockEnd: `calc(${vars.space.xl} + ${fallbackVar(selectionBarSpace, '0px')})`,
  background: vars.color.surface.page,
  color: vars.color.text.primary,
  touchAction: 'manipulation',
  selectors: {
    '&:focus-visible': { ...focusOutline, outlineOffset: vars.focusRing.insetOffset }
  }
})

// Keeps the last items clear of the floating selection bar.
export const reserveSelection = style({
  vars: { [selectionBarSpace]: `calc(${vars.density.row} + ${vars.space.xxl})` }
})

/** Holds a virtual window at the full height of everything it could draw. */
export const spacer = style({
  position: 'relative'
})

export const window = style({
  willChange: 'transform'
})

export const empty = style({
  display: 'flex',
  alignItems: 'center',
  justifyContent: 'center',
  margin: 0,
  paddingBlock: `calc(${vars.space.xxl} * 2)`,
  paddingInline: vars.layout.contentPad,
  color: vars.color.text.secondary,
  ...typography('bodyLarge')
})

import { createContainer, style } from '@vanilla-extract/css'
import { focusOutline, typography, vars } from '@/shared/theme'

// The browse view's content area declares this container; the tree narrows when it is small.
export const hostContainer = createContainer()

export const root = style({
  flex: 'none',
  width: '240px',
  overflowY: 'auto',
  paddingBlock: vars.space.sm,
  borderInlineEnd: 'none',
  background: vars.color.surface.container,
  color: vars.color.text.primary,
  '@container': {
    [`${hostContainer} (max-width: 839.98px)`]: {
      width: '200px'
    }
  }
})

export const tree = style({
  listStyle: 'none',
  margin: 0,
  padding: 0,
  width: '100%'
})

export const list = style({
  minWidth: 0
})

// The tree as a drawer on compact layouts, where the list leaves no room beside it.
export const overlay = style({
  paddingBlockEnd: vars.space.sm
})

export const treeRowMore = style({
  selectors: {
    '&:focus-visible': focusOutline
  },
  display: 'flex',
  alignItems: 'center',
  width: '100%',
  minHeight: vars.density.control,
  paddingBlock: vars.space.xs,
  border: 0,
  background: 'transparent',
  textAlign: 'start',
  cursor: 'pointer',
  ...typography('label')
})

export const treeRowStatus = style({
  display: 'flex',
  alignItems: 'center',
  minHeight: vars.density.control,
  margin: 0,
  paddingBlock: vars.space.xs,
  overflowWrap: 'anywhere',
  color: vars.color.text.secondary,
  ...typography('bodySmall')
})

export const overlayHeader = style({
  position: 'sticky',
  insetBlockStart: 0,
  zIndex: 1,
  display: 'flex',
  justifyContent: 'flex-end',
  padding: vars.space.xs,
  borderBlockEnd: `${vars.stroke.thin} solid ${vars.color.border.subtle}`,
  background: vars.color.surface.container
})

export const treeRowStatusError = style({
  color: vars.color.danger.solid
})

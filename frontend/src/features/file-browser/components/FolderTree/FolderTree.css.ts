import { createContainer, style } from '@vanilla-extract/css'
import { vars } from '@/shared/theme'

// The browse view's content area declares this container; the tree narrows when it is small.
export const hostContainer = createContainer()

export const root = style({
  flex: '0 0 240px',
  width: '240px',
  overflowY: 'auto',
  paddingBlock: '8px',
  borderInlineEnd: 'none',
  background: vars.color.surface.container,
  color: vars.color.text.primary,
  '@container': {
    [`${hostContainer} (max-width: 839.98px)`]: {
      flexBasis: '200px',
      width: '200px'
    }
  }
})

export const tree = style({
  listStyle: 'none',
  margin: '0',
  padding: '0',
  width: '100%'
})

export const list = style({
  minWidth: '0'
})

// The tree as a drawer on compact layouts, where the list leaves no room beside it.
export const overlay = style({
  paddingBlockEnd: vars.space.sm
})

export const treeRowMore = style({
  selectors: {
    '&:focus-visible': {
      outline: `${vars.focusRing.width} solid ${vars.color.focus}`,
      outlineOffset: vars.focusRing.offset
    }
  },
  display: 'flex',
  alignItems: 'center',
  width: '100%',
  minHeight: vars.density.control,
  paddingBlock: '4px',
  border: '0',
  background: 'transparent',
  textAlign: 'start',
  cursor: 'pointer',
  fontSize: vars.typography.label.size,
  lineHeight: vars.typography.label.lineHeight
})

export const treeRowStatus = style({
  selectors: {
    [`${overlay} &`]: {
      minHeight: vars.density.control
    }
  },
  display: 'flex',
  alignItems: 'center',
  minHeight: '28px',
  margin: '0',
  paddingBlock: '4px',
  overflowWrap: 'anywhere',
  color: vars.color.text.secondary,
  fontSize: vars.typography.bodySmall.size,
  lineHeight: vars.typography.bodySmall.lineHeight
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

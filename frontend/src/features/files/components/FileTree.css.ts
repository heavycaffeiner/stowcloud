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

export const overlay = style({
  position: 'fixed',
  top: '0',
  bottom: `calc(${vars.layout.navBar} + env(safe-area-inset-bottom, 0px))`,
  insetInlineStart: '0',
  margin: '0',
  maxWidth: 'min(320px, 85vw)',
  width: '100%',
  height: 'auto',
  padding: '0',
  border: 'none',
  boxShadow: vars.elevation.md,
  translate: '0 0',
  transition: `translate ${vars.motion.easing}, display ${vars.motion.medium} allow-discrete, overlay ${vars.motion.medium} allow-discrete`,
  selectors: {
    '&:not([open])': {
      translate: '-100% 0'
    },
    '&[open]': {
      '@starting-style': {
        translate: '-100% 0'
      }
    },
    '&::backdrop': {
      background: `color-mix(in srgb, ${vars.color.scrim} 32%, transparent)`,
      transition: `background-color ${vars.motion.easing}, display ${vars.motion.medium} allow-discrete, overlay ${vars.motion.medium} allow-discrete`
    },
    '&:not([open])::backdrop': {
      background: `color-mix(in srgb, ${vars.color.scrim} 0%, transparent)`
    },
    '&[open]::backdrop': {
      '@starting-style': {
        background: `color-mix(in srgb, ${vars.color.scrim} 0%, transparent)`
      }
    }
  }
})

export const treeRowMore = style({
  selectors: {
    [`${overlay} &`]: {
      minHeight: '44px'
    },
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
      minHeight: '44px'
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
  top: '0',
  zIndex: '1',
  display: 'flex',
  alignItems: 'center',
  justifyContent: 'flex-end',
  height: '56px',
  paddingInline: '8px',
  boxShadow: `0 1px 0 ${vars.color.border.subtle}`,
  background: vars.color.surface.page
})

export const overlayClose = style({
  display: 'inline-flex',
  alignItems: 'center',
  justifyContent: 'center',
  width: vars.density.control,
  height: vars.density.control,
  padding: '0',
  border: '0',
  borderRadius: '50%',
  background: 'transparent',
  color: 'inherit',
  fontSize: '1.5rem',
  lineHeight: '1',
  cursor: 'pointer'
})

export const treeRowStatusError = style({
  color: vars.color.danger.solid
})

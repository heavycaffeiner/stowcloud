import { createContainer, style } from '@vanilla-extract/css'
import { vars } from '../../ui/theme.css'

// The browse view's content area declares this container; the tree narrows when it is small.
export const hostContainer = createContainer()

export const root = style({
  flex: '0 0 240px',
  width: '240px',
  overflowY: 'auto',
  paddingBlock: '8px',
  borderInlineEnd: 'none',
  background: `rgb(${vars.color.surfaceContainerLow})`,
  color: vars.content.primary,
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
  bottom: `calc(${vars.layout.navBarHeight} + env(safe-area-inset-bottom, 0px))`,
  insetInlineStart: '0',
  margin: '0',
  maxWidth: 'min(320px, 85vw)',
  width: '100%',
  height: 'auto',
  padding: '0',
  border: 'none',
  boxShadow: vars.elevation.level2,
  translate: '0 0',
  transition: `translate ${vars.motion.easingStandard}, display ${vars.motion.durationMedium2} allow-discrete, overlay ${vars.motion.durationMedium2} allow-discrete`,
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
      background: `color-mix(in srgb, rgb(${vars.color.scrim}) 32%, transparent)`,
      transition: `background-color ${vars.motion.easingStandard}, display ${vars.motion.durationMedium2} allow-discrete, overlay ${vars.motion.durationMedium2} allow-discrete`
    },
    '&:not([open])::backdrop': {
      background: `color-mix(in srgb, rgb(${vars.color.scrim}) 0%, transparent)`
    },
    '&[open]::backdrop': {
      '@starting-style': {
        background: `color-mix(in srgb, rgb(${vars.color.scrim}) 0%, transparent)`
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
      outline: `${vars.focusRing.width} solid ${vars.state.focus}`,
      outlineOffset: vars.focusRing.offset
    }
  },
  display: 'flex',
  alignItems: 'center',
  width: '100%',
  minHeight: vars.control.min,
  paddingBlock: '4px',
  border: '0',
  background: 'transparent',
  textAlign: 'start',
  cursor: 'pointer',
  fontSize: vars.typescale.labelLarge.size,
  lineHeight: vars.typescale.labelLarge.lineHeight
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
  color: vars.content.secondary,
  fontSize: vars.typescale.bodySmall.size,
  lineHeight: vars.typescale.bodySmall.lineHeight
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
  boxShadow: `0 1px 0 rgb(${vars.color.outlineVariant})`,
  background: vars.surface.page
})

export const overlayClose = style({
  display: 'inline-flex',
  alignItems: 'center',
  justifyContent: 'center',
  width: vars.control.min,
  height: vars.control.min,
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
  color: `rgb(${vars.color.error})`
})

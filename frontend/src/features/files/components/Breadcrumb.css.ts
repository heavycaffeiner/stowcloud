import { style } from '@vanilla-extract/css'
import * as browseViewStyles from './BrowseView.css'
import { vars } from '@/shared/theme'

export const root = style({
  display: 'flex',
  flex: '1 1 auto',
  alignItems: 'center',
  minWidth: '0',
  maxWidth: '100%',
  height: '40px',
  fontSize: vars.typography.label.size,
  fontWeight: vars.typography.label.weight,
  lineHeight: vars.typography.label.lineHeight,
  letterSpacing: vars.typography.label.tracking,
  '@media': {
    '(max-width: 599.98px)': {
      height: '44px'
    }
  },
  selectors: {
    [`${browseViewStyles.folderHeading} > &`]: {
      flexBasis: 'auto'
    }
  }
})

export const list = style({
  display: 'flex',
  flex: '1 1 auto',
  alignItems: 'center',
  minWidth: '0',
  width: '100%',
  margin: '0',
  padding: '0',
  listStyle: 'none'
})

export const item = style({
  display: 'inline-flex',
  flex: '0 1 auto',
  alignItems: 'center',
  minWidth: '0',
  height: '40px',
  whiteSpace: 'nowrap',
  '@media': {
    '(max-width: 599.98px)': {
      height: '44px'
    }
  }
})

export const itemRoot = style({
  maxWidth: 'min(144px, 22%)',
  '@media': {
    '(max-width: 599.98px)': {
      maxWidth: '30%'
    }
  }
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

export const link = style({
  display: 'inline-flex',
  alignItems: 'center',
  height: '40px',
  boxSizing: 'border-box',
  paddingInline: '8px',
  maxWidth: '100%',
  borderRadius: vars.radius.sm,
  font: 'inherit',
  textAlign: 'start',
  flex: '1 1 auto',
  minWidth: '0',
  width: '100%',
  border: '0',
  background: 'transparent',
  color: vars.color.accent.solid,
  cursor: 'pointer',
  transition: 'background-color 120ms ease, color 120ms ease',
  selectors: {
    '&:hover': {
      background: `color-mix(in srgb, ${vars.color.accent.solid} 8%, transparent)`
    },
    '&:active': {
      background: `color-mix(in srgb, ${vars.color.accent.solid} 14%, transparent)`
    },
    '&:focus-visible': {
      outline: `${vars.focusRing.width} solid ${vars.color.focus}`,
      outlineOffset: vars.focusRing.offset
    }
  },
  '@media': {
    '(max-width: 599.98px)': {
      height: '44px',
      paddingInline: '6px'
    }
  }
})

export const current = style({
  display: 'inline-flex',
  flex: '1 1 auto',
  alignItems: 'center',
  minWidth: '0',
  height: '40px',
  boxSizing: 'border-box',
  paddingInline: '8px',
  maxWidth: '100%',
  borderRadius: vars.radius.sm,
  font: 'inherit',
  textAlign: 'start',
  color: vars.color.text.primary,
  '@media': {
    '(max-width: 599.98px)': {
      height: '44px',
      paddingInline: '6px'
    }
  }
})

export const label = style({
  display: 'block',
  minWidth: '0',
  overflow: 'hidden',
  textOverflow: 'ellipsis',
  whiteSpace: 'nowrap',
  selectors: {
    [`${itemRoot} &`]: {
      color: vars.color.accent.solid,
      fontWeight: '600'
    }
  }
})

export const sep = style({
  display: 'inline-flex',
  flex: 'none',
  alignItems: 'center',
  justifyContent: 'center',
  height: '40px',
  paddingInline: '2px',
  color: vars.color.text.secondary,
  font: 'inherit',
  opacity: '.55',
  userSelect: 'none',
  '@media': {
    '(max-width: 599.98px)': {
      height: '44px'
    }
  }
})

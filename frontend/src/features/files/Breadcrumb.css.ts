import { style } from '@vanilla-extract/css'
import * as browseViewStyles from './routes/BrowseView.css'
import { vars } from '../../ui/theme.css'

export const root = style({
  display: 'flex',
  flex: '1 1 auto',
  alignItems: 'center',
  minWidth: '0',
  maxWidth: '100%',
  height: '40px',
  fontSize: vars.typescale.labelLarge.size,
  fontWeight: vars.typescale.labelLarge.weight,
  lineHeight: vars.typescale.labelLarge.lineHeight,
  letterSpacing: vars.typescale.labelLarge.tracking,
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
  borderRadius: vars.shape.cornerSmall,
  font: 'inherit',
  textAlign: 'start',
  flex: '1 1 auto',
  minWidth: '0',
  width: '100%',
  border: '0',
  background: 'transparent',
  color: `rgb(${vars.color.primary})`,
  cursor: 'pointer',
  transition: 'background-color 120ms ease, color 120ms ease',
  selectors: {
    '&:hover': {
      background: `rgba(${vars.color.primary}, 0.08)`
    },
    '&:active': {
      background: `rgba(${vars.color.primary}, 0.14)`
    },
    '&:focus-visible': {
      outline: `${vars.focusRing.width} solid ${vars.state.focus}`,
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
  borderRadius: vars.shape.cornerSmall,
  font: 'inherit',
  textAlign: 'start',
  color: `rgb(${vars.color.onSurface})`,
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
      color: `rgb(${vars.color.primary})`,
      fontWeight: '600'
    }
  }
})

export const ellipsisBtn = style({
  selectors: {
    '&:focus-visible': {
      outline: `${vars.focusRing.width} solid ${vars.state.focus}`,
      outlineOffset: vars.focusRing.offset
    },
    '&:hover': {
      background: `rgb(${vars.color.surfaceContainerHighest})`,
      color: `rgb(${vars.color.onSurface})`
    }
  },
  display: 'inline-flex',
  flex: 'none',
  alignItems: 'center',
  justifyContent: 'center',
  width: '40px',
  height: '40px',
  padding: '0',
  border: '0',
  borderRadius: vars.shape.cornerSmall,
  background: 'transparent',
  color: `rgb(${vars.color.onSurfaceVariant})`,
  cursor: 'pointer',
  font: 'inherit',
  fontWeight: '700',
  letterSpacing: '.08em',
  transition: 'background-color 120ms ease, color 120ms ease',
  '@media': {
    '(max-width: 599.98px)': {
      width: '44px',
      height: '44px'
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
  color: `rgb(${vars.color.onSurfaceVariant})`,
  font: 'inherit',
  opacity: '.55',
  userSelect: 'none',
  '@media': {
    '(max-width: 599.98px)': {
      height: '44px'
    }
  }
})

export const menu = style({
  boxSizing: 'border-box',
  width: 'max-content',
  minWidth: '200px',
  maxWidth: 'min(320px, calc(100vw - 16px))'
})

export const menuItem = style({
  display: 'flex',
  alignItems: 'center',
  gap: '8px',
  width: '100%',
  minWidth: '0'
})

export const menuIcon = style({
  flex: 'none'
})

export const menuLabel = style({
  minWidth: '0',
  overflowWrap: 'anywhere',
  whiteSpace: 'normal'
})

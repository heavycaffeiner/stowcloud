import { style } from '@vanilla-extract/css'
import * as recentPageStyles from '../features/recent/routes/RecentPage.css'
import { vars } from './theme.css'

export const error = style({
  margin: '0 0 16px',
  padding: '12px 16px',
  overflowWrap: 'anywhere',
  borderRadius: '8px',
  background: vars.state.error,
  color: vars.state.errorContent
})

export const root = style({
  height: '100%',
  minWidth: '0',
  overflowX: 'hidden',
  overflowY: 'auto',
  wordBreak: 'keep-all'
})

export const inner = style({
  display: 'flex',
  flexDirection: 'column',
  width: 'min(100%, 860px)',
  minWidth: '0',
  minHeight: '100%',
  marginInline: 'auto',
  padding: vars.layout.pagePad
})

export const header = style({
  display: 'flex',
  alignItems: 'center',
  gap: '8px',
  marginBottom: '16px',
  flex: 'none',
  minWidth: '0',
  '@media': {
    '(max-width: 599.98px)': {
      marginBottom: '12px'
    }
  }
})

export const title = style({
  flex: '1',
  minWidth: '0',
  margin: '0',
  overflowWrap: 'anywhere',
  fontSize: vars.typescale.headlineSmall.size,
  fontWeight: vars.typescale.headlineSmall.weight,
  lineHeight: vars.typescale.headlineSmall.lineHeight,
  letterSpacing: vars.typescale.headlineSmall.tracking
})

export const coverage = style({
  margin: '-8px 0 16px',
  color: vars.content.secondary,
  fontSize: vars.typescale.bodySmall.size,
  fontWeight: vars.typescale.bodySmall.weight,
  lineHeight: vars.typescale.bodySmall.lineHeight,
  letterSpacing: vars.typescale.bodySmall.tracking,
  overflowWrap: 'anywhere'
})

export const list = style({
  display: 'flex',
  flexDirection: 'column',
  margin: '0',
  padding: '0',
  listStyle: 'none',
  minWidth: '0'
})

export const row = style({
  display: 'flex',
  alignItems: 'center',
  gap: '12px',
  width: '100%',
  minHeight: '56px',
  padding: '8px 16px',
  border: '0',
  borderBottom: `1px solid ${vars.outline.variant}`,
  background: 'none',
  color: 'inherit',
  font: 'inherit',
  textAlign: 'start',
  cursor: 'pointer',
  selectors: {
    '&:hover': {
      background: vars.surface.container
    },
    '&:focus-visible': {
      outline: `${vars.focusRing.width} solid ${vars.state.focus}`,
      outlineOffset: vars.focusRing.offset
    }
  },
  '@media': {
    '(max-width: 599.98px)': {
      gap: '8px',
      paddingInline: '8px'
    }
  }
})

export const routeIconButton = style({
  selectors: {
    '&:focus-visible': {
      outline: `${vars.focusRing.width} solid ${vars.state.focus}`,
      outlineOffset: vars.focusRing.offset
    },
    '&:disabled': {
      cursor: 'default',
      opacity: '0.38'
    }
  },
  display: 'inline-flex',
  alignItems: 'center',
  justifyContent: 'center',
  width: '40px',
  height: '40px',
  padding: '0',
  border: '0',
  borderRadius: '50%',
  background: 'transparent',
  color: 'inherit',
  cursor: 'pointer',
  flex: 'none',
  '@media': {
    '(max-width: 599.98px)': {
      width: '44px',
      height: '44px'
    }
  }
})

export const icon = style({
  display: 'inline-flex',
  flex: 'none',
  color: vars.content.secondary
})

export const text = style({
  display: 'flex',
  flex: '1',
  flexDirection: 'column',
  minWidth: '0'
})

export const name = style({
  overflow: 'hidden',
  textOverflow: 'ellipsis',
  whiteSpace: 'nowrap'
})

export const path = style({
  overflow: 'hidden',
  textOverflow: 'ellipsis',
  color: vars.content.secondary,
  fontSize: vars.typescale.bodySmall.size,
  fontWeight: vars.typescale.bodySmall.weight,
  lineHeight: vars.typescale.bodySmall.lineHeight,
  letterSpacing: vars.typescale.bodySmall.tracking,
  whiteSpace: 'nowrap'
})

export const meta = style({
  color: vars.content.secondary,
  fontSize: vars.typescale.bodySmall.size,
  fontWeight: vars.typescale.bodySmall.weight,
  lineHeight: vars.typescale.bodySmall.lineHeight,
  letterSpacing: vars.typescale.bodySmall.tracking,
  whiteSpace: 'nowrap',
  flexShrink: '0',
  '@media': {
    '(max-width: 599.98px)': {
      selectors: {
        [`${recentPageStyles.root} &`]: {
          display: 'none'
        }
      }
    },
    '(max-width: 480px)': {
      display: 'none'
    }
  }
})

export const loading = style({
  display: 'grid',
  flex: '1 1 12rem',
  placeItems: 'center',
  minHeight: '8rem',
  margin: '0',
  padding: '32px',
  color: vars.content.secondary,
  textAlign: 'center',
  '@media': {
    '(max-width: 599.98px)': {
      padding: '24px 16px'
    }
  }
})

export const empty = style({
  display: 'grid',
  flex: '1 1 12rem',
  placeItems: 'center',
  minHeight: '8rem',
  margin: '0',
  padding: '32px',
  color: vars.content.secondary,
  textAlign: 'center',
  '@media': {
    '(max-width: 599.98px)': {
      padding: '24px 16px'
    }
  }
})

export const routeIconButtonDanger = style({
  color: `rgb(${vars.color.error})`
})

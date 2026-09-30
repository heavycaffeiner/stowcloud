import { style } from '@vanilla-extract/css'
import { vars } from './theme.css'

export const root = style({
  position: 'fixed',
  zIndex: '40',
  inset: 'auto 0 0',
  height: `calc(${vars.layout.navBarHeight} + env(safe-area-inset-bottom, 0px))`,
  boxSizing: 'border-box',
  display: 'flex',
  alignItems: 'stretch',
  justifyContent: 'center',
  gap: '4px',
  padding:
    '4px max(8px, env(safe-area-inset-right, 0px)) calc(4px + env(safe-area-inset-bottom, 0px)) max(8px, env(safe-area-inset-left, 0px))',
  background: vars.surface.container,
  borderTop: `1px solid ${vars.outline.variant}`,
  boxShadow: '0 -4px 16px rgba(0, 0, 0, 0.12)',
  userSelect: 'none'
})

export const item = style({
  flex: '1 1 0',
  minWidth: '0',
  maxWidth: '7rem',
  minHeight: '48px',
  display: 'flex',
  flexDirection: 'column',
  alignItems: 'center',
  justifyContent: 'center',
  gap: '2px',
  padding: '2px 2px 4px',
  border: '0',
  borderRadius: '16px',
  background: 'transparent',
  color: vars.content.secondary,
  cursor: 'pointer',
  font: 'inherit',
  fontSize: vars.typescale.labelMedium.size,
  lineHeight: vars.typescale.labelMedium.lineHeight,
  fontWeight: vars.typescale.labelMedium.weight,
  outline: 'none',
  transition: 'background-color 150ms ease, color 150ms ease',
  selectors: {
    '&:focus-visible': {
      outline: `${vars.focusRing.width} solid ${vars.state.focus}`,
      outlineOffset: vars.focusRing.offset
    }
  }
})

export const itemActive = style({
  selectors: {
    [`${item}&`]: {
      color: vars.state.selectionContent,
      fontWeight: '600'
    }
  }
})

export const icon = style({
  width: '64px',
  height: '32px',
  display: 'grid',
  placeItems: 'center',
  flex: 'none',
  boxSizing: 'border-box',
  borderRadius: '16px',
  transition: 'background-color 150ms ease',
  selectors: {
    [`${item}${itemActive} &`]: {
      background: vars.state.selection
    }
  }
})

export const label = style({
  width: '100%',
  minWidth: '0',
  height: '1rem',
  flex: '0 0 1rem',
  overflow: 'hidden',
  textAlign: 'center',
  textOverflow: 'ellipsis',
  whiteSpace: 'nowrap'
})

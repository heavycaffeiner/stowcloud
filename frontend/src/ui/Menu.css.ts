import { style } from '@vanilla-extract/css'
import { fadeIn, scaleUp, vars } from './theme.css'

export const menuShell = style({
  zIndex: '50',
  maxInlineSize: 'calc(100vw - 16px)',
  maxBlockSize: 'calc(100vh - 16px)',
  overflowY: 'auto',
  transformOrigin: 'top right',
  borderRadius: vars.radius.medium,
  overflow: 'hidden'
})

export const menu = style({
  minInlineSize: '200px',
  maxInlineSize: 'min(360px, calc(100vw - 16px))',
  borderRadius: vars.radius.medium,
  overflow: 'hidden',
  boxShadow: vars.shadow.level3,
  selectors: {
    '&::part(panel)': {
      borderRadius: vars.radius.medium,
      overflow: 'hidden'
    }
  }
})

export const sheetScrim = style({
  position: 'fixed',
  inset: '0',
  zIndex: '49',
  background: `rgba(${vars.color.scrim}, 0.36)`,
  animation: `${fadeIn} 150ms ease`
})

export const sheet = style({
  position: 'fixed',
  inset: 'auto 0 0',
  zIndex: '50',
  boxSizing: 'border-box',
  inlineSize: '100vw',
  maxInlineSize: '100vw',
  maxBlockSize: '80dvh',
  margin: '0',
  padding: '0',
  overflow: 'hidden',
  border: '0',
  borderRadius: `${vars.radius.large} ${vars.radius.large} 0 0`,
  background: `rgb(${vars.color.surfaceContainerLow})`,
  color: `rgb(${vars.color.onSurface})`,
  boxShadow: vars.shadow.level3,
  translate: '0 100%',
  selectors: {
    '&[open]': {
      translate: '0 0'
    },
    '&::backdrop': {
      background: `rgba(${vars.color.scrim}, 0.32)`
    }
  },
  '@media': {
    '(prefers-reduced-motion: no-preference)': {
      transition:
        'translate 220ms cubic-bezier(0.2, 0, 0, 1), overlay 220ms allow-discrete, display 220ms allow-discrete',
      selectors: {
        '&[open]': {
          '@starting-style': {
            translate: '0 100%'
          }
        }
      }
    }
  }
})

export const sheetHandleWrap = style({
  display: 'flex',
  justifyContent: 'center',
  padding: '8px 0'
})

export const sheetHandle = style({
  inlineSize: '32px',
  blockSize: '4px',
  borderRadius: vars.radius.full,
  background: `rgb(${vars.color.outlineVariant})`
})

export const sheetContent = style({
  display: 'flex',
  flexDirection: 'column',
  gap: '4px',
  padding: '8px 12px env(safe-area-inset-bottom, 0)',
  overflow: 'auto'
})

export const list = style({
  selectors: {
    [`${sheetContent} > &`]: {
      inlineSize: '100%'
    }
  },
  display: 'grid',
  minWidth: '200px',
  padding: '8px',
  animation: `${scaleUp} 150ms cubic-bezier(0.2, 0, 0, 1)`,
  transformOrigin: 'top left'
})

export const item = style({
  minHeight: vars.control.min,
  padding: '8px 12px',
  border: '0',
  borderRadius: vars.shape.cornerExtraSmall,
  background: 'transparent',
  color: 'inherit',
  textAlign: 'start',
  cursor: 'pointer',
  transition: 'background-color 120ms ease, transform 100ms ease',
  selectors: {
    '&:active': {
      transform: 'scale(0.98)'
    },
    '&:hover': {
      background: 'color-mix(in srgb, currentColor 10%, transparent)'
    },
    '&:focus-visible': {
      background: 'color-mix(in srgb, currentColor 10%, transparent)',
      outline: `${vars.focusRing.width} solid ${vars.state.focus}`,
      outlineOffset: vars.focusRing.offset
    }
  }
})

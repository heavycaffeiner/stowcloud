import { style } from '@vanilla-extract/css'
import { vars } from './theme.css'

export const root = style({
  selectors: {
    '&::part(panel)': {
      borderRadius: vars.radius.large,
      boxShadow: vars.shadow.level4
    },
    '&::part(action)': {
      display: 'flex',
      alignItems: 'center',
      justifyContent: 'flex-end',
      gap: '8px'
    }
  }
})

export const actions = style({
  display: 'inline-flex',
  alignItems: 'center',
  verticalAlign: 'middle',
  gap: '8px'
})

// Lifts mdui's 35rem cap so the panel can grow with its content up to the viewport.
export const wide = style({
  selectors: {
    '&::part(panel)': {
      maxInlineSize: 'calc(100vw - 32px)'
    }
  },
  '@media': {
    '(max-width: 599.98px)': {
      selectors: {
        '&::part(panel)': {
          maxInlineSize: 'calc(100vw - 16px)'
        }
      }
    }
  }
})

// Fills the viewport up to a cap and hands the whole panel to the body, for media that draws its own chrome.
export const viewer = style({
  padding:
    'max(16px, env(safe-area-inset-top, 0px)) max(16px, env(safe-area-inset-right, 0px)) max(16px, env(safe-area-inset-bottom, 0px)) max(16px, env(safe-area-inset-left, 0px))',
  boxSizing: 'border-box',
  selectors: {
    '&::part(panel)': {
      width: 'min(75rem, 100%)',
      height: '100%',
      minWidth: '0',
      maxWidth: '100%',
      maxHeight: '56rem',
      padding: '0',
      overflow: 'hidden'
    },
    '&::part(body)': {
      display: 'flex',
      flex: '1',
      minHeight: '0',
      margin: '0',
      overflow: 'hidden'
    }
  },
  '@media': {
    '(min-width: 840px)': {
      padding: '32px'
    }
  }
})

import { style } from '@vanilla-extract/css'
import { vars } from './theme.css'

export const page = style({
  flex: '1',
  width: 'min(100%, 60rem)',
  minWidth: '0',
  marginInline: 'auto',
  padding: vars.layout.pagePad,
  wordBreak: 'normal'
})

export const header = style({
  paddingBlockEnd: '4px'
})

export const title = style({
  margin: '0',
  color: `rgb(${vars.color.onSurface})`,
  fontSize: vars.typescale.headlineSmall.size,
  fontWeight: vars.typescale.headlineSmall.weight,
  letterSpacing: vars.typescale.headlineSmall.tracking,
  lineHeight: vars.typescale.headlineSmall.lineHeight
})

export const tabs = style({
  display: 'flex',
  gap: '4px',
  margin: '16px 0 24px',
  padding: '4px',
  overflowX: 'auto',
  '@media': {
    '(max-width: 599.98px)': {
      marginBlockEnd: '16px'
    }
  }
})

export const tab = style({
  display: 'flex',
  flex: 'none',
  flexDirection: 'column',
  alignItems: 'center',
  justifyContent: 'center',
  gap: '4px',
  minInlineSize: '48px',
  minBlockSize: '64px',
  padding: '10px 12px',
  border: 'none',
  borderRadius: vars.radius.small,
  background: 'transparent',
  color: `rgb(${vars.color.onSurfaceVariant})`,
  fontSize: vars.typescale.labelLarge.size,
  fontWeight: vars.typescale.labelLarge.weight,
  lineHeight: vars.typescale.labelLarge.lineHeight,
  letterSpacing: vars.typescale.labelLarge.tracking,
  whiteSpace: 'nowrap',
  cursor: 'pointer',
  transition: 'background-color 150ms ease',
  selectors: {
    '&:hover': {
      background: `rgb(${vars.color.surfaceContainerHigh})`
    },
    '&[aria-current="page"]': {
      background: `rgb(${vars.color.secondaryContainer})`,
      color: `rgb(${vars.color.onSecondaryContainer})`
    },
    '&:focus-visible': {
      outline: `${vars.focusRing.width} solid ${vars.state.focus}`,
      outlineOffset: vars.focusRing.offset
    }
  },
  '@media': {
    '(max-width: 599.98px)': {
      paddingInline: '8px'
    }
  }
})

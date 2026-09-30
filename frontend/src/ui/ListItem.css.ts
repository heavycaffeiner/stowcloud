import { createVar, fallbackVar, style } from '@vanilla-extract/css'
import { vars } from './theme.css'

export const trailingJustify = createVar()

export const trailingWidth = createVar()

export const root = style({
  display: 'flex',
  flexWrap: 'wrap',
  alignItems: 'center',
  gap: '16px',
  minBlockSize: '56px',
  padding: '8px 16px',
  color: `rgb(${vars.color.onSurface})`,
  selectors: {
    '&:has(:focus-visible)': {
      outline: `${vars.focusRing.width} solid ${vars.state.focus}`,
      outlineOffset: vars.focusRing.insetOffset
    }
  },
  '@media': {
    '(max-width: 599px)': {
      gap: '12px',
      paddingInline: '12px'
    }
  }
})

export const clickable = style({
  cursor: 'pointer'
})

export const selected = style({
  background: `rgb(${vars.color.secondaryContainer})`,
  color: `rgb(${vars.color.onSecondaryContainer})`
})

export const leading = style({
  display: 'inline-flex',
  alignItems: 'center',
  justifyContent: 'center',
  flex: '0 0 32px',
  inlineSize: '32px',
  blockSize: '32px'
})

export const text = style({
  display: 'flex',
  flex: '1 0 12rem',
  flexDirection: 'column',
  minInlineSize: '0',
  '@media': {
    '(max-width: 599px)': {
      flexBasis: '10rem'
    }
  }
})

export const headline = style({
  display: 'flex',
  alignItems: 'center',
  flexWrap: 'wrap',
  gap: '8px',
  minInlineSize: '0',
  overflowWrap: 'anywhere',
  fontSize: vars.typescale.bodyLarge.size,
  fontWeight: vars.typescale.bodyLarge.weight,
  lineHeight: vars.typescale.bodyLarge.lineHeight
})

export const supporting = style({
  color: `rgb(${vars.color.onSurfaceVariant})`,
  fontSize: vars.typescale.bodyMedium.size,
  lineHeight: vars.typescale.bodyMedium.lineHeight,
  overflowWrap: 'anywhere'
})

export const trailing = style({
  display: 'inline-flex',
  flexWrap: 'wrap',
  alignItems: 'center',
  justifyContent: fallbackVar(trailingJustify, 'flex-end'),
  gap: '8px',
  marginInlineStart: 'auto',
  maxInlineSize: '100%',
  width: fallbackVar(trailingWidth, 'auto')
})

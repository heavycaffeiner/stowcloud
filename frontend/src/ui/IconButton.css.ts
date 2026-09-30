import { createVar, fallbackVar, style } from '@vanilla-extract/css'
import { vars } from './theme.css'

export const iconButtonSize = createVar()

export const root = style({
  alignItems: 'center',
  justifyContent: 'center',
  verticalAlign: 'middle',
  position: 'relative',
  minInlineSize: vars.control.min,
  minBlockSize: vars.control.min,
  display: 'inline-flex',
  flex: 'none',
  transition: 'transform 120ms cubic-bezier(0.2, 0, 0, 1)',
  selectors: {
    '&:active': {
      transform: 'scale(0.92)'
    }
  }
})

export const button = style({
  display: 'inline-flex',
  alignItems: 'center',
  justifyContent: 'center',
  inlineSize: fallbackVar(iconButtonSize, vars.control.min),
  blockSize: fallbackVar(iconButtonSize, vars.control.min),
  selectors: {
    '&::part(button):focus-visible': {
      outline: `${vars.focusRing.width} solid ${vars.state.focus}`,
      outlineOffset: vars.focusRing.offset
    }
  }
})

export const tip = style({
  maxInlineSize: '240px',
  '@media': {
    '(hover: none), (pointer: coarse)': {
      display: 'none'
    }
  },
  position: 'fixed',
  zIndex: '40',
  inset: 'auto',
  margin: '0',
  border: 'none',
  transform: 'translateX(-50%)',
  maxWidth: '240px',
  padding: '4px 8px',
  borderRadius: vars.shape.cornerExtraSmall,
  background: `rgb(${vars.color.inverseSurface})`,
  color: `rgb(${vars.color.inverseOnSurface})`,
  fontSize: vars.typescale.bodySmall.size,
  fontWeight: vars.typescale.bodySmall.weight,
  letterSpacing: vars.typescale.bodySmall.tracking,
  lineHeight: vars.typescale.bodySmall.lineHeight,
  whiteSpace: 'nowrap',
  overflow: 'hidden',
  textOverflow: 'ellipsis',
  pointerEvents: 'none',
  opacity: '0',
  transition: 'opacity 120ms ease, transform 120ms ease'
})

export const tipPlaced = style({
  opacity: '1'
})

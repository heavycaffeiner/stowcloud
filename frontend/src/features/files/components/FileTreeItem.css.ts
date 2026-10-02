import { style } from '@vanilla-extract/css'
import * as fileTreeStyles from './FileTree.css'
import { vars } from '../../../ui/theme.css'

export const root = style({
  selectors: {
    [`${fileTreeStyles.overlay} &`]: {
      height: '44px'
    }
  },
  display: 'flex',
  alignItems: 'center',
  height: '40px',
  borderRadius: vars.shape.cornerFull,
  color: vars.content.primary,
  transition: 'background-color 150ms ease, color 150ms ease',
  '@media': {
    '(prefers-reduced-motion: reduce)': {
      transition: 'none'
    }
  }
})

export const ancestor = style({
  background: `color-mix(in srgb, rgb(${vars.color.secondaryContainer}) 40%, transparent)`
})

export const active = style({
  background: vars.state.selection,
  color: vars.state.selectionContent,
  fontWeight: '600'
})

export const icon = style({
  color: vars.content.icon
})

export const twisty = style({
  display: 'inline-flex',
  alignItems: 'center',
  justifyContent: 'center',
  flex: `0 0 ${vars.control.min}`,
  width: vars.control.min,
  height: vars.control.min,
  padding: '0',
  border: 'none',
  borderRadius: vars.shape.cornerFull,
  background: 'transparent',
  color: vars.content.secondary,
  cursor: 'pointer',
  transition: 'background-color 150ms ease',
  selectors: {
    '&:hover': {
      background: 'color-mix(in srgb, currentColor 8%, transparent)'
    },
    '&:focus-visible': {
      outline: `${vars.focusRing.width} solid ${vars.state.focus}`,
      outlineOffset: vars.focusRing.offset
    }
  },
  '@media': {
    '(prefers-reduced-motion: reduce)': {
      transition: 'none'
    }
  }
})

export const twistyIcon = style({
  display: 'inline-flex',
  transition: 'transform 150ms ease',
  '@media': {
    '(prefers-reduced-motion: reduce)': {
      transition: 'none'
    }
  }
})

export const twistyIconExpanded = style({
  transform: 'rotate(90deg)'
})

export const label = style({
  display: 'flex',
  alignItems: 'center',
  gap: '8px',
  flex: '1',
  minWidth: '0',
  height: '100%',
  paddingInline: '0 8px',
  border: 'none',
  background: 'transparent',
  color: 'inherit',
  fontSize: vars.typescale.bodyMedium.size,
  fontWeight: vars.typescale.bodyMedium.weight,
  lineHeight: vars.typescale.bodyMedium.lineHeight,
  letterSpacing: vars.typescale.bodyMedium.tracking,
  textAlign: 'start',
  cursor: 'pointer',
  selectors: {
    '&:focus-visible': {
      outline: `${vars.focusRing.width} solid ${vars.state.focus}`,
      outlineOffset: vars.focusRing.offset
    }
  }
})

export const name = style({
  overflow: 'hidden',
  textOverflow: 'ellipsis',
  whiteSpace: 'nowrap'
})

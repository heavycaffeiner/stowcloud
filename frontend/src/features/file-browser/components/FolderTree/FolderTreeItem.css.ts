import { style } from '@vanilla-extract/css'
import * as folderTreeStyles from './FolderTree.css'
import { vars } from '@/shared/theme'

export const root = style({
  selectors: {
    [`${folderTreeStyles.overlay} &`]: {
      height: '44px'
    }
  },
  display: 'flex',
  alignItems: 'center',
  height: '40px',
  borderRadius: vars.radius.full,
  color: vars.color.text.primary,
  transition: 'background-color 150ms ease, color 150ms ease',
  '@media': {
    '(prefers-reduced-motion: reduce)': {
      transition: 'none'
    }
  }
})

export const ancestor = style({
  background: `color-mix(in srgb, ${vars.color.selection.bg} 40%, transparent)`
})

export const active = style({
  background: vars.color.selection.bg,
  color: vars.color.selection.fg,
  fontWeight: '600'
})

export const icon = style({
  color: vars.color.text.icon
})

export const twisty = style({
  display: 'inline-flex',
  alignItems: 'center',
  justifyContent: 'center',
  flex: `0 0 ${vars.density.control}`,
  width: vars.density.control,
  height: vars.density.control,
  padding: '0',
  border: 'none',
  borderRadius: vars.radius.full,
  background: 'transparent',
  color: vars.color.text.secondary,
  cursor: 'pointer',
  transition: 'background-color 150ms ease',
  selectors: {
    '&:hover': {
      background: 'color-mix(in srgb, currentColor 8%, transparent)'
    },
    '&:focus-visible': {
      outline: `${vars.focusRing.width} solid ${vars.color.focus}`,
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
  fontSize: vars.typography.body.size,
  fontWeight: vars.typography.body.weight,
  lineHeight: vars.typography.body.lineHeight,
  letterSpacing: vars.typography.body.tracking,
  textAlign: 'start',
  cursor: 'pointer',
  selectors: {
    '&:focus-visible': {
      outline: `${vars.focusRing.width} solid ${vars.color.focus}`,
      outlineOffset: vars.focusRing.offset
    }
  }
})

export const name = style({
  overflow: 'hidden',
  textOverflow: 'ellipsis',
  whiteSpace: 'nowrap'
})

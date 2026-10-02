import { style } from '@vanilla-extract/css'
import { focusOutline, typography, vars } from '@/shared/theme'
import * as folderTreeStyles from './FolderTree.css'

const ease = `${vars.motion.short} ${vars.motion.easing}`

export const root = style({
  selectors: {
    [`${folderTreeStyles.overlay} &`]: {
      height: vars.density.controlCompact
    }
  },
  display: 'flex',
  alignItems: 'center',
  height: vars.density.control,
  borderRadius: vars.radius.full,
  color: vars.color.text.primary,
  transition: `background-color ${ease}, color ${ease}`
})

export const ancestor = style({
  background: `color-mix(in srgb, ${vars.color.selection.bg} 40%, transparent)`
})

export const active = style({
  background: vars.color.selection.bg,
  color: vars.color.selection.fg,
  fontWeight: vars.font.weight.bold
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
  padding: 0,
  border: 'none',
  borderRadius: vars.radius.full,
  background: 'transparent',
  color: vars.color.text.secondary,
  cursor: 'pointer',
  transition: `background-color ${ease}`,
  selectors: {
    '&:hover': {
      background: 'color-mix(in srgb, currentColor 8%, transparent)'
    },
    '&:focus-visible': focusOutline
  }
})

export const twistyIcon = style({
  display: 'inline-flex',
  transition: `transform ${ease}`
})

export const twistyIconExpanded = style({
  transform: 'rotate(90deg)'
})

export const label = style({
  display: 'flex',
  alignItems: 'center',
  gap: vars.space.sm,
  flex: 1,
  minWidth: 0,
  height: '100%',
  paddingInline: `0 ${vars.space.sm}`,
  border: 'none',
  background: 'transparent',
  color: 'inherit',
  ...typography('body'),
  textAlign: 'start',
  cursor: 'pointer',
  selectors: {
    '&:focus-visible': focusOutline
  }
})

export const name = style({
  overflow: 'hidden',
  textOverflow: 'ellipsis',
  whiteSpace: 'nowrap'
})

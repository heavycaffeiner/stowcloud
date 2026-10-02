import { style } from '@vanilla-extract/css'
import { focusOutline, typography, vars } from '@/shared/theme'

export const root = style({
  position: 'fixed',
  zIndex: 10,
  insetBlock: `${vars.layout.header} 0`,
  insetInlineStart: 0,
  inlineSize: vars.layout.navDrawer,
  display: 'flex',
  flexDirection: 'column',
  overflow: 'hidden',
  background: `color-mix(in srgb, ${vars.color.surface.container} 78%, ${vars.color.surface.page})`,
  borderInlineEnd: `${vars.stroke.thin} solid ${vars.color.border.subtle}`,
  color: vars.color.text.primary,
  transition: `inline-size ${vars.motion.medium} ${vars.motion.easing}`
})

export const collapsed = style({
  inlineSize: vars.layout.navDrawerCollapsed
})

// The compact layout's drawer: the header stays put while the body scrolls.
export const overlay = style({
  display: 'flex',
  flexDirection: 'column',
  overflow: 'hidden'
})

export const overlayHeader = style({
  flex: 'none',
  display: 'flex',
  alignItems: 'center',
  gap: vars.space.sm,
  blockSize: vars.layout.header,
  paddingInline: vars.space.sm,
  borderBlockEnd: `${vars.stroke.thin} solid ${vars.color.border.subtle}`
})

export const appName = style({
  flex: 1,
  color: vars.color.text.primary,
  ...typography('title')
})

export const body = style({
  flex: 1,
  minBlockSize: 0,
  display: 'flex',
  flexDirection: 'column',
  overflowX: 'hidden',
  overflowY: 'auto',
  overscrollBehavior: 'contain',
  scrollbarWidth: 'thin',
  scrollbarColor: `color-mix(in srgb, ${vars.color.text.secondary} 35%, transparent) transparent`,
  selectors: { [`${collapsed} &`]: { paddingBlockStart: vars.space.md } }
})

export const newWrap = style({
  flex: 'none',
  padding: `${vars.space.lg} ${vars.space.md} ${vars.space.md}`,
  selectors: {
    [`${collapsed} &`]: { display: 'flex', justifyContent: 'center', paddingInline: 0 }
  }
})

// Even padding on both sides keeps the icon and label centered in the full-width button.
export const newBtn = style({
  inlineSize: '100%',
  paddingInline: vars.space.xl
})

export const sectionHeader = style({
  display: 'flex',
  alignItems: 'center',
  justifyContent: 'space-between',
  padding: `${vars.space.sm} ${vars.space.sm} ${vars.space.xs} ${vars.space.lg}`
})

export const sectionTitle = style({
  margin: `${vars.space.md} ${vars.space.lg} ${vars.space.sm}`,
  color: vars.color.text.secondary,
  textTransform: 'uppercase',
  ...typography('labelSmall'),
  selectors: { [`${sectionHeader} &`]: { margin: 0 } }
})

export const divider = style({
  blockSize: vars.stroke.thin,
  margin: `${vars.space.sm} ${vars.space.md}`,
  background: vars.color.border.subtle
})

export const list = style({
  display: 'flex',
  flexDirection: 'column',
  gap: vars.space.xxs,
  margin: 0,
  paddingBlock: 0,
  paddingInline: vars.space.sm,
  listStyle: 'none',
  selectors: { [`${collapsed} &`]: { paddingBlock: vars.space.sm, paddingInline: 0 } }
})

export const sublist = style({
  display: 'flex',
  flexDirection: 'column',
  gap: vars.space.xxs,
  margin: 0,
  padding: 0,
  listStyle: 'none'
})

export const entry = style({
  margin: 0
})

// One navigation row, for a destination or a root folder.
export const item = style({
  display: 'flex',
  alignItems: 'center',
  gap: vars.space.md,
  inlineSize: '100%',
  minBlockSize: vars.density.control,
  paddingBlock: 0,
  paddingInline: vars.space.md,
  border: 0,
  borderRadius: vars.radius.md,
  background: 'transparent',
  color: vars.color.text.secondary,
  font: 'inherit',
  ...typography('label'),
  textAlign: 'start',
  cursor: 'pointer',
  transition: `background-color ${vars.motion.short} ${vars.motion.easing}, color ${vars.motion.short} ${vars.motion.easing}`,
  selectors: {
    '&:hover': { background: vars.color.surface.fill, color: vars.color.text.primary },
    '&:focus-visible': focusOutline,
    [`${collapsed} &`]: {
      justifyContent: 'center',
      inlineSize: vars.density.control,
      padding: 0,
      marginInline: 'auto'
    }
  }
})

export const itemActive = style({
  background: vars.color.selection.bg,
  color: vars.color.selection.fg,
  selectors: {
    '&:hover': {
      background: `color-mix(in srgb, ${vars.color.selection.bg} 88%, ${vars.color.text.primary})`,
      color: vars.color.selection.fg
    }
  }
})

export const itemIcon = style({
  display: 'inline-flex',
  flex: 'none',
  alignItems: 'center',
  justifyContent: 'center'
})

export const itemLabel = style({
  flex: 1,
  minInlineSize: 0,
  overflow: 'hidden',
  textOverflow: 'ellipsis',
  whiteSpace: 'nowrap'
})

export const reorderRow = style({
  display: 'flex',
  alignItems: 'center',
  gap: vars.space.sm,
  minBlockSize: vars.density.row,
  paddingBlock: vars.space.xxs,
  paddingInline: vars.space.md,
  color: vars.color.text.secondary,
  ...typography('body')
})

export const reorderActions = style({
  display: 'flex',
  alignItems: 'center',
  gap: vars.space.xxs,
  marginInlineStart: 'auto'
})

export const reorderChevron = style({
  display: 'flex',
  alignItems: 'center',
  justifyContent: 'center'
})

export const reorderChevronUp = style({
  rotate: '-90deg'
})

export const reorderChevronDown = style({
  rotate: '90deg'
})

export const reorderError = style({
  margin: `0 ${vars.space.md}`,
  padding: `${vars.space.xs} ${vars.space.md}`,
  color: vars.color.danger.solid,
  ...typography('bodySmall')
})

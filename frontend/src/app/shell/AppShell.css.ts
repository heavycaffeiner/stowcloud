import { style } from '@vanilla-extract/css'
import { focusOutline, typography, vars } from '@/shared/theme'

export const root = style({
  width: '100%',
  minWidth: 0,
  height: ['100vh', '100dvh'],
  display: 'flex',
  flexDirection: 'column',
  overflow: 'hidden',
  background: vars.color.surface.page
})

export const compact = style({
  flexDirection: 'column',
  vars: {
    [vars.density.control]: vars.density.controlCompact
  }
})

export const header = style({
  position: 'sticky',
  top: 0,
  left: 0,
  right: 0,
  height: vars.layout.header,
  minHeight: vars.layout.header,
  zIndex: 30,
  display: 'flex',
  alignItems: 'center',
  justifyContent: 'space-between',
  padding: `0 ${vars.space.lg}`,
  background: `color-mix(in srgb, ${vars.color.surface.page} 96%, transparent)`,
  backdropFilter: 'blur(12px)',
  borderBottom: `${vars.stroke.thin} solid ${vars.color.border.subtle}`,
  color: vars.color.text.primary,
  boxSizing: 'border-box',
  selectors: {
    [`${compact} &`]: {
      paddingTop: 'env(safe-area-inset-top, 0px)',
      paddingRight: `max(${vars.space.sm}, env(safe-area-inset-right, 0px))`,
      paddingLeft: `max(${vars.space.sm}, env(safe-area-inset-left, 0px))`,
      gap: vars.space.sm,
      height: `calc(${vars.layout.header} + env(safe-area-inset-top, 0px))`
    }
  }
})

export const headerLeft = style({
  selectors: {
    [`${compact} &`]: {
      flex: '1 1 auto',
      minWidth: 0
    }
  },
  display: 'flex',
  alignItems: 'center',
  gap: vars.space.sm,
  flex: 'none'
})

export const headerBrandBtn = style({
  selectors: {
    [`${compact} &`]: {
      minWidth: 0,
      flex: '1 1 auto'
    },
    '&:focus-visible': focusOutline
  },
  background: 'none',
  border: 'none',
  padding: `0 ${vars.space.xs}`,
  cursor: 'pointer',
  display: 'flex',
  alignItems: 'center',
  color: 'inherit'
})

export const headerBrand = style({
  selectors: {
    [`${compact} &`]: {
      display: 'block',
      minWidth: 0,
      overflow: 'hidden',
      textOverflow: 'ellipsis',
      whiteSpace: 'nowrap'
    }
  },
  ...typography('titleLarge'),
  fontWeight: vars.font.weight.bold,
  color: vars.color.text.primary,
  letterSpacing: '-0.02em'
})

export const headerCenter = style({
  selectors: {
    [`${compact} &`]: {
      flex: `0 0 ${vars.density.controlCompact}`,
      width: vars.density.controlCompact,
      minWidth: vars.density.controlCompact,
      margin: 0
    }
  },
  flex: '1 1 auto',
  maxWidth: '600px',
  margin: `0 ${vars.space.xl}`,
  display: 'flex',
  justifyContent: 'center'
})

export const headerSearch = style({
  selectors: {
    [`${compact} &`]: {
      width: vars.density.controlCompact,
      height: vars.density.controlCompact,
      minWidth: vars.density.controlCompact,
      padding: 0,
      boxSizing: 'border-box',
      justifyContent: 'center'
    },
    '&:hover': {
      background: vars.color.surface.overlay,
      borderColor: vars.color.border.strong
    },
    '&:focus-visible': focusOutline,
    '&:active': {
      transform: 'scale(0.99)'
    }
  },
  width: '100%',
  height: vars.density.controlDesktop,
  display: 'flex',
  alignItems: 'center',
  gap: vars.space.sm,
  padding: `0 ${vars.space.md}`,
  border: `${vars.stroke.thin} solid ${vars.color.border.subtle}`,
  borderRadius: vars.radius.md,
  background: vars.color.surface.container,
  color: vars.color.text.secondary,
  cursor: 'pointer',
  textAlign: 'left',
  transition: `background-color ${vars.motion.short} ${vars.motion.easing}, border-color ${vars.motion.short} ${vars.motion.easing}, box-shadow ${vars.motion.short} ${vars.motion.easing}`
})

export const headerSearchPlaceholder = style({
  selectors: {
    [`${compact} &`]: {
      display: 'none'
    }
  },
  flex: 1,
  minWidth: 0,
  overflow: 'hidden',
  textOverflow: 'ellipsis',
  whiteSpace: 'nowrap',
  ...typography('label'),
  color: vars.color.text.secondary
})

export const headerSearchHints = style({
  selectors: {
    [`${compact} &`]: {
      display: 'none'
    }
  },
  display: 'flex',
  alignItems: 'center',
  gap: vars.space.sm,
  flex: 'none'
})

export const headerRight = style({
  selectors: {
    [`${compact} &`]: {
      flex: 'none',
      minWidth: 0,
      gap: 0
    }
  },
  display: 'flex',
  alignItems: 'center',
  gap: vars.space.xs,
  flex: 'none'
})

export const headerSearchIcon = style({
  display: 'inline-flex',
  alignItems: 'center',
  color: vars.color.text.secondary,
  flex: 'none'
})

export const headerShortcut = style({
  fontFamily: 'inherit',
  ...typography('labelSmall'),
  padding: `0 ${vars.space.sm}`,
  borderRadius: vars.radius.xs,
  background: vars.color.surface.overlay,
  border: `${vars.stroke.thin} solid ${vars.color.border.subtle}`,
  color: vars.color.text.secondary
})

export const headerFilterIcon = style({
  display: 'inline-flex',
  alignItems: 'center',
  color: vars.color.text.secondary
})

export const avatar = style({
  display: 'inline-flex',
  alignItems: 'center',
  justifyContent: 'center',
  inlineSize: vars.space.xxl,
  blockSize: vars.space.xxl,
  border: `${vars.stroke.thin} solid ${vars.color.border.subtle}`,
  borderRadius: vars.radius.full,
  background: vars.color.selection.bg,
  color: vars.color.selection.fg,
  ...typography('labelSmall')
})

export const body = style({
  flex: 1,
  minHeight: 0,
  display: 'flex',
  overflow: 'hidden',
  position: 'relative'
})

export const main = style({
  flex: 1,
  minWidth: 0,
  display: 'flex',
  flexDirection: 'column',
  overflowY: 'auto',
  overflowX: 'hidden',
  background: vars.color.surface.page,
  transition: `padding-left ${vars.motion.medium} ${vars.motion.easing}`,
  selectors: {
    [`${compact} &`]: {
      paddingLeft: 0,
      paddingBottom: `calc(${vars.layout.navBar} + env(safe-area-inset-bottom, 0px))`
    }
  }
})

export const mainDrawer = style({
  paddingLeft: vars.layout.navDrawer
})

export const mainCollapsed = style({
  paddingLeft: vars.layout.navDrawerCollapsed
})

export const trayStack = style({
  position: 'fixed',
  right: vars.space.xl,
  bottom: vars.space.xl,
  zIndex: 30,
  display: 'flex',
  flexDirection: 'column',
  alignItems: 'flex-end',
  gap: vars.space.md
})

export const trayStackCompact = style({
  right: `max(${vars.space.lg}, env(safe-area-inset-right, 0px))`,
  bottom: `calc(${vars.space.xl} + ${vars.layout.navBar} + env(safe-area-inset-bottom, 0px))`
})

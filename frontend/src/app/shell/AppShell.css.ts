import { style } from '@vanilla-extract/css'
import { vars } from '../../ui/theme.css'

export const root = style({
  width: '100%',
  minWidth: '0',
  height: ['100vh', '100dvh'],
  display: 'flex',
  flexDirection: 'column',
  overflow: 'hidden',
  background: vars.surface.page
})

export const compact = style({
  flexDirection: 'column',
  vars: {
    [vars.control.min]: vars.control.minCompact
  }
})

export const header = style({
  position: 'sticky',
  top: '0',
  left: '0',
  right: '0',
  height: vars.layout.headerHeight,
  minHeight: vars.layout.headerHeight,
  zIndex: '30',
  display: 'flex',
  alignItems: 'center',
  justifyContent: 'space-between',
  padding: '0 20px',
  background: `color-mix(in srgb, ${vars.surface.page} 96%, transparent)`,
  backdropFilter: 'blur(12px)',
  borderBottom: `1px solid ${vars.outline.variant}`,
  color: vars.content.primary,
  boxSizing: 'border-box',
  selectors: {
    [`${compact} &`]: {
      paddingTop: 'env(safe-area-inset-top, 0px)',
      paddingRight: 'max(8px, env(safe-area-inset-right, 0px))',
      paddingLeft: 'max(8px, env(safe-area-inset-left, 0px))',
      gap: '8px',
      height: `calc(${vars.layout.headerHeight} + env(safe-area-inset-top, 0px))`
    }
  }
})

export const headerLeft = style({
  selectors: {
    [`${compact} &`]: {
      flex: '1 1 auto',
      minWidth: '0'
    }
  },
  display: 'flex',
  alignItems: 'center',
  gap: '8px',
  flex: 'none'
})

export const headerBrandBtn = style({
  selectors: {
    [`${compact} &`]: {
      minWidth: '0',
      flex: '1 1 auto'
    },
    '&:focus-visible': {
      outline: `${vars.focusRing.width} solid ${vars.state.focus}`,
      outlineOffset: vars.focusRing.offset
    }
  },
  background: 'none',
  border: 'none',
  padding: '0 4px',
  cursor: 'pointer',
  display: 'flex',
  alignItems: 'center',
  color: 'inherit'
})

export const headerBrand = style({
  selectors: {
    [`${compact} &`]: {
      display: 'block',
      minWidth: '0',
      overflow: 'hidden',
      textOverflow: 'ellipsis',
      whiteSpace: 'nowrap'
    }
  },
  fontSize: vars.typescale.titleLarge.size,
  lineHeight: vars.typescale.titleLarge.lineHeight,
  fontWeight: '700',
  color: vars.content.primary,
  letterSpacing: '-0.02em'
})

export const headerCenter = style({
  selectors: {
    [`${compact} &`]: {
      flex: '0 0 44px',
      width: '44px',
      minWidth: '44px',
      margin: '0'
    }
  },
  flex: '1 1 auto',
  maxWidth: '600px',
  margin: '0 24px',
  display: 'flex',
  justifyContent: 'center'
})

export const headerSearch = style({
  selectors: {
    [`${compact} &`]: {
      width: '44px',
      height: '44px',
      minWidth: '44px',
      padding: '0',
      boxSizing: 'border-box',
      justifyContent: 'center'
    },
    '&:hover': {
      background: vars.surface.overlay,
      borderColor: vars.outline.default
    },
    '&:focus-visible': {
      outline: `${vars.focusRing.width} solid ${vars.state.focus}`,
      outlineOffset: vars.focusRing.offset
    },
    '&:active': {
      transform: 'scale(0.99)'
    }
  },
  width: '100%',
  height: '40px',
  display: 'flex',
  alignItems: 'center',
  gap: '10px',
  padding: '0 14px',
  border: `1px solid ${vars.outline.variant}`,
  borderRadius: '12px',
  background: vars.surface.container,
  color: vars.content.secondary,
  cursor: 'pointer',
  textAlign: 'left',
  transition: 'background-color 150ms ease, border-color 150ms ease, box-shadow 150ms ease'
})

export const headerSearchPlaceholder = style({
  selectors: {
    [`${compact} &`]: {
      display: 'none'
    }
  },
  flex: '1',
  minWidth: '0',
  overflow: 'hidden',
  textOverflow: 'ellipsis',
  whiteSpace: 'nowrap',
  fontSize: vars.typescale.labelLarge.size,
  lineHeight: vars.typescale.labelLarge.lineHeight,
  color: vars.content.secondary
})

export const headerSearchHints = style({
  selectors: {
    [`${compact} &`]: {
      display: 'none'
    }
  },
  display: 'flex',
  alignItems: 'center',
  gap: '8px',
  flex: 'none'
})

export const headerRight = style({
  selectors: {
    [`${compact} &`]: {
      flex: 'none',
      minWidth: '0',
      gap: '0'
    }
  },
  display: 'flex',
  alignItems: 'center',
  gap: '6px',
  flex: 'none'
})

export const headerAvatarBtn = style({
  selectors: {
    [`${compact} &`]: {
      marginLeft: '0'
    },
    '&:focus-visible': {
      outline: `${vars.focusRing.width} solid ${vars.state.focus}`,
      outlineOffset: vars.focusRing.offset
    }
  },
  width: '44px',
  height: '44px',
  display: 'inline-flex',
  alignItems: 'center',
  justifyContent: 'center',
  border: 'none',
  background: 'none',
  padding: '0',
  cursor: 'pointer',
  borderRadius: '50%',
  marginLeft: '4px'
})

export const headerMenuBtn = style({
  width: vars.control.min,
  height: vars.control.min,
  display: 'inline-flex',
  alignItems: 'center',
  justifyContent: 'center',
  borderRadius: '50%',
  border: 'none',
  background: 'transparent',
  color: vars.content.primary,
  cursor: 'pointer',
  transition: 'background-color 150ms ease',
  selectors: {
    '&:hover': {
      background: `color-mix(in srgb, ${vars.content.primary} 8%, transparent)`
    },
    '&:focus-visible': {
      outline: `${vars.focusRing.width} solid ${vars.state.focus}`,
      outlineOffset: vars.focusRing.offset
    }
  }
})

export const headerSearchIcon = style({
  display: 'inline-flex',
  alignItems: 'center',
  color: vars.content.secondary,
  flex: 'none'
})

export const headerShortcut = style({
  fontFamily: 'inherit',
  fontSize: vars.typescale.labelMedium.size,
  lineHeight: vars.typescale.labelMedium.lineHeight,
  padding: '1px 7px',
  borderRadius: '4px',
  background: vars.surface.overlay,
  border: `1px solid ${vars.outline.variant}`,
  color: vars.content.secondary
})

export const headerFilterIcon = style({
  display: 'inline-flex',
  alignItems: 'center',
  color: vars.content.secondary
})

export const headerIconBtn = style({
  width: vars.control.min,
  height: vars.control.min,
  display: 'inline-flex',
  alignItems: 'center',
  justifyContent: 'center',
  borderRadius: '50%',
  border: 'none',
  background: 'transparent',
  color: vars.content.primary,
  cursor: 'pointer',
  transition: 'background-color 150ms ease',
  selectors: {
    '&:hover': {
      background: `color-mix(in srgb, ${vars.content.primary} 8%, transparent)`
    },
    '&:focus-visible': {
      outline: `${vars.focusRing.width} solid ${vars.state.focus}`,
      outlineOffset: vars.focusRing.offset
    }
  }
})

export const headerAvatar = style({
  width: '32px',
  height: '32px',
  borderRadius: '50%',
  background: vars.state.selection,
  color: vars.state.selectionContent,
  fontSize: vars.typescale.labelMedium.size,
  lineHeight: vars.typescale.labelMedium.lineHeight,
  fontWeight: '600',
  display: 'inline-flex',
  alignItems: 'center',
  justifyContent: 'center',
  border: `1px solid ${vars.outline.variant}`,
  transition: 'transform 120ms ease',
  selectors: {
    [`${headerAvatarBtn}:hover &`]: {
      transform: 'scale(1.05)'
    }
  }
})

export const headerAccountWrap = style({
  position: 'relative',
  display: 'flex',
  alignItems: 'center'
})

export const headerAccountMenu = style({
  position: 'absolute',
  top: 'calc(100% + 8px)',
  right: '0',
  zIndex: '60',
  minWidth: '200px',
  padding: '6px',
  display: 'flex',
  flexDirection: 'column',
  gap: '2px',
  border: `1px solid ${vars.outline.variant}`,
  borderRadius: '12px',
  background: vars.surface.overlay,
  boxShadow: '0 12px 32px rgba(0, 0, 0, 0.28)'
})

export const headerAccountName = style({
  padding: '8px 10px 6px',
  fontSize: vars.typescale.labelLarge.size,
  lineHeight: vars.typescale.labelLarge.lineHeight,
  fontWeight: '600',
  color: vars.content.primary,
  overflow: 'hidden',
  textOverflow: 'ellipsis',
  whiteSpace: 'nowrap'
})

export const headerAccountItem = style({
  display: 'flex',
  alignItems: 'center',
  gap: '10px',
  minHeight: vars.control.min,
  padding: '0 10px',
  border: 'none',
  borderRadius: '8px',
  background: 'transparent',
  color: vars.content.primary,
  font: 'inherit',
  fontSize: vars.typescale.labelLarge.size,
  lineHeight: vars.typescale.labelLarge.lineHeight,
  textAlign: 'left',
  cursor: 'pointer',
  transition: 'background-color 120ms ease',
  selectors: {
    '&:hover': {
      background: `color-mix(in srgb, ${vars.content.primary} 8%, transparent)`
    },
    '&:focus-visible': {
      outline: `${vars.focusRing.width} solid ${vars.state.focus}`,
      outlineOffset: vars.focusRing.offset
    }
  }
})

export const body = style({
  flex: '1',
  minHeight: '0',
  display: 'flex',
  overflow: 'hidden',
  position: 'relative'
})

export const main = style({
  flex: '1',
  minWidth: '0',
  display: 'flex',
  flexDirection: 'column',
  overflowY: 'auto',
  overflowX: 'hidden',
  background: vars.surface.page,
  transition: 'padding-left 200ms cubic-bezier(0.2, 0, 0, 1)',
  selectors: {
    [`${compact} &`]: {
      paddingLeft: '0',
      paddingBottom: `calc(${vars.layout.navBarHeight} + env(safe-area-inset-bottom, 0px))`
    }
  }
})

export const mainDrawer = style({
  paddingLeft: vars.layout.navDrawerWidth
})

export const mainCollapsed = style({
  paddingLeft: vars.layout.navDrawerCollapsedWidth
})

export const trayStack = style({
  position: 'fixed',
  right: '24px',
  bottom: '24px',
  zIndex: '30',
  display: 'flex',
  flexDirection: 'column',
  alignItems: 'flex-end',
  gap: '12px'
})

export const trayStackCompact = style({
  right: 'max(16px, env(safe-area-inset-right, 0px))',
  bottom: `calc(24px + ${vars.layout.navBarHeight} + env(safe-area-inset-bottom, 0px))`
})

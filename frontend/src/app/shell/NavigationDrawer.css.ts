import { style } from '@vanilla-extract/css'
import { vars } from '../../ui/theme.css'

export const root = style({
  position: 'fixed',
  zIndex: '10',
  top: vars.layout.headerHeight,
  bottom: '0',
  left: '0',
  width: vars.layout.navDrawerWidth,
  display: 'flex',
  flexDirection: 'column',
  overflow: 'hidden',
  background: `color-mix(in srgb, ${vars.surface.container} 78%, ${vars.surface.page})`,
  borderRight: `1px solid ${vars.outline.variant}`,
  color: vars.content.primary,
  transition: 'width 200ms cubic-bezier(0.2, 0, 0, 1), transform 220ms cubic-bezier(0.2, 0, 0, 1)'
})

export const collapsed = style({
  width: vars.layout.navDrawerCollapsedWidth
})

export const body = style({
  minHeight: '0',
  flex: '1',
  display: 'flex',
  flexDirection: 'column',
  overflowY: 'auto',
  overflowX: 'hidden',
  overscrollBehavior: 'contain',
  scrollbarWidth: 'thin',
  scrollbarColor: `color-mix(in srgb, ${vars.content.secondary} 35%, transparent) transparent`,
  selectors: {
    [`${collapsed} &`]: {
      paddingTop: '12px'
    }
  }
})

export const newWrap = style({
  padding: '16px 12px 12px',
  flex: 'none',
  order: '-2'
})

export const rootsSection = style({
  order: '-1'
})

export const newBtn = style({
  width: '100%',
  height: '44px',
  borderRadius: '12px',
  background: vars.surface.raised,
  border: `1px solid ${vars.outline.variant}`,
  color: vars.content.primary,
  display: 'flex',
  alignItems: 'center',
  justifyContent: 'center',
  gap: '14px',
  padding: '0 13px',
  cursor: 'pointer',
  fontSize: vars.typescale.labelLarge.size,
  lineHeight: vars.typescale.labelLarge.lineHeight,
  fontWeight: '500',
  textAlign: 'center',
  transition: 'background-color 150ms ease, transform 100ms ease',
  selectors: {
    '&:hover': {
      background: vars.surface.overlay
    },
    '&:active': {
      transform: 'scale(0.98)'
    },
    '&:focus-visible': {
      outline: `${vars.focusRing.width} solid ${vars.state.focus}`,
      outlineOffset: vars.focusRing.offset
    }
  }
})

export const newBtnCollapsed = style({
  width: '44px',
  height: '44px',
  borderRadius: '12px',
  padding: '0',
  justifyContent: 'center',
  margin: '0 auto'
})

export const sectionHeader = style({
  display: 'flex',
  alignItems: 'center',
  justifyContent: 'space-between',
  padding: '8px 16px 4px',
  minHeight: '32px'
})

export const sectionTitle = style({
  margin: '12px 16px 6px 16px',
  color: vars.content.secondary,
  fontSize: vars.typescale.labelMedium.size,
  lineHeight: vars.typescale.labelMedium.lineHeight,
  fontWeight: '600',
  letterSpacing: '0.04em',
  textTransform: 'uppercase',
  selectors: {
    [`${sectionHeader} &`]: {
      margin: '0'
    }
  }
})

export const list = style({
  listStyle: 'none',
  margin: '0',
  display: 'flex',
  flexDirection: 'column',
  gap: '2px',
  padding: '0 10px',
  selectors: {
    [`${collapsed} &`]: {
      padding: '8px 0'
    }
  }
})

export const sublist = style({
  listStyle: 'none',
  margin: '0',
  display: 'flex',
  flexDirection: 'column',
  gap: '2px',
  padding: '0'
})

export const entry = style({
  margin: '0'
})

export const item = style({
  width: '100%',
  minHeight: vars.control.min,
  padding: '0 14px',
  display: 'flex',
  alignItems: 'center',
  gap: '14px',
  border: '0',
  borderRadius: '10px',
  background: 'transparent',
  color: vars.content.secondary,
  cursor: 'pointer',
  font: 'inherit',
  fontSize: vars.typescale.labelLarge.size,
  lineHeight: vars.typescale.labelLarge.lineHeight,
  fontWeight: '500',
  textAlign: 'left',
  transition: 'background-color 140ms ease, color 140ms ease',
  selectors: {
    '&:hover': {
      background: `color-mix(in srgb, ${vars.content.primary} 6%, transparent)`,
      color: vars.content.primary
    },
    [`${collapsed} &`]: {
      width: '44px',
      height: '44px',
      minHeight: '44px',
      padding: '0',
      justifyContent: 'center',
      borderRadius: '12px',
      margin: '2px auto'
    },
    '&:focus-visible': {
      outline: `${vars.focusRing.width} solid ${vars.state.focus}`,
      outlineOffset: vars.focusRing.offset
    },
    '&:active': {
      transform: 'scale(0.99)'
    }
  }
})

export const itemActive = style({
  background: vars.state.selection,
  color: vars.state.selectionContent,
  fontWeight: '600',
  selectors: {
    '&:hover': {
      background: `color-mix(in srgb, ${vars.state.selection} 88%, ${vars.content.primary})`,
      color: vars.state.selectionContent
    }
  }
})

export const itemLabel = style({
  minWidth: '0',
  flex: '1',
  overflow: 'hidden',
  textOverflow: 'ellipsis',
  whiteSpace: 'nowrap'
})

export const divider = style({
  height: '1px',
  background: vars.outline.variant,
  margin: '10px 12px'
})

export const subitem = style({
  width: '100%',
  minHeight: '40px',
  padding: '0 14px',
  display: 'flex',
  alignItems: 'center',
  gap: '14px',
  border: '0',
  borderRadius: '10px',
  background: 'transparent',
  color: vars.content.secondary,
  cursor: 'pointer',
  font: 'inherit',
  fontSize: vars.typescale.labelLarge.size,
  lineHeight: vars.typescale.labelLarge.lineHeight,
  textAlign: 'left',
  transition: 'background-color 140ms ease, color 140ms ease',
  selectors: {
    '&:hover': {
      background: `color-mix(in srgb, ${vars.content.primary} 6%, transparent)`,
      color: vars.content.primary
    },
    '&:focus-visible': {
      outline: `${vars.focusRing.width} solid ${vars.state.focus}`,
      outlineOffset: vars.focusRing.offset
    },
    '&:active': {
      transform: 'scale(0.99)'
    }
  }
})

export const subitemActive = style({
  background: vars.state.selection,
  color: vars.state.selectionContent,
  fontWeight: '600',
  selectors: {
    '&:hover': {
      background: `color-mix(in srgb, ${vars.state.selection} 88%, ${vars.content.primary})`,
      color: vars.state.selectionContent
    }
  }
})

export const itemIcon = style({
  display: 'inline-flex',
  alignItems: 'center',
  justifyContent: 'center',
  flex: 'none',
  selectors: {
    [`${itemActive} &`]: {
      color: vars.state.selectionContent
    },
    [`${subitemActive} &`]: {
      color: vars.state.selectionContent
    }
  }
})

export const subitemLabel = style({
  minWidth: '0',
  flex: '1',
  overflow: 'hidden',
  textOverflow: 'ellipsis',
  whiteSpace: 'nowrap'
})

export const reorderToggle = style({
  minHeight: vars.control.min,
  display: 'inline-flex',
  alignItems: 'center',
  background: 'transparent',
  border: 'none',
  color: `rgb(${vars.color.primary})`,
  fontSize: vars.typescale.labelLarge.size,
  lineHeight: vars.typescale.labelLarge.lineHeight,
  cursor: 'pointer',
  padding: '0 8px',
  borderRadius: '8px',
  selectors: {
    '&:focus-visible': {
      outline: `${vars.focusRing.width} solid ${vars.state.focus}`,
      outlineOffset: vars.focusRing.offset
    }
  }
})

export const subitemReorder = style({
  display: 'flex',
  alignItems: 'center',
  gap: '8px',
  minHeight: '48px',
  padding: '2px 12px',
  fontSize: vars.typescale.bodyMedium.size,
  lineHeight: vars.typescale.bodyMedium.lineHeight
})

export const reorderActions = style({
  display: 'flex',
  alignItems: 'center',
  gap: '2px',
  marginLeft: 'auto'
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
  margin: '0 12px',
  padding: '4px 12px',
  color: vars.state.errorContent,
  fontSize: vars.typescale.bodySmall.size,
  lineHeight: vars.typescale.bodySmall.lineHeight
})

export const overlay = style({
  position: 'fixed',
  inset: '0 auto 0 0',
  top: '0',
  bottom: '0',
  width: 'min(300px, 85vw)',
  maxWidth: 'none',
  height: ['100vh', '100dvh'],
  minHeight: '100dvh',
  margin: '0',
  padding: '0',
  border: 'none',
  borderRadius: '0 16px 16px 0',
  background: vars.surface.container,
  boxShadow: '4px 0 24px rgba(0, 0, 0, 0.35)',
  zIndex: '60',
  display: 'flex',
  flexDirection: 'column',
  overflow: 'hidden',
  selectors: {
    '&::backdrop': {
      background: `rgba(${vars.color.scrim}, 0.5)`,
      backdropFilter: 'blur(4px)'
    }
  }
})

export const overlayHeader = style({
  height: '56px',
  display: 'flex',
  alignItems: 'center',
  justifyContent: 'space-between',
  padding: '0 16px',
  borderBottom: `1px solid ${vars.outline.variant}`,
  flex: 'none'
})

export const appName = style({
  fontSize: vars.typescale.titleMedium.size,
  lineHeight: vars.typescale.titleMedium.lineHeight,
  fontWeight: '700',
  color: vars.content.primary
})

export const userAvatar = style({
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
  border: `1px solid ${vars.outline.variant}`
})

export const overlayClose = style({
  width: '44px',
  height: '44px',
  display: 'inline-flex',
  alignItems: 'center',
  justifyContent: 'center',
  borderRadius: '50%',
  border: 'none',
  background: 'transparent',
  color: vars.content.primary,
  cursor: 'pointer',
  selectors: {
    '&:focus-visible': {
      outline: `${vars.focusRing.width} solid ${vars.state.focus}`,
      outlineOffset: vars.focusRing.offset
    }
  }
})

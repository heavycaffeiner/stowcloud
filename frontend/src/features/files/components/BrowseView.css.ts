import { fallbackVar, keyframes, style } from '@vanilla-extract/css'
import * as appShellStyles from '../../../app/shell/AppShell.css'
import { hostContainer } from './FileTree.css'
import { fadeInUp, trayStackTop, vars } from '../../../ui/theme.css'

const snackbarEnter = keyframes({
  from: {
    opacity: '0',
    transform: 'translate(-50%, 12px) scale(0.96)'
  },
  to: {
    opacity: '1',
    transform: 'translate(-50%, 0) scale(1)'
  }
})

export const filterPill = style({
  selectors: {
    '&:focus-visible': {
      outline: `${vars.focusRing.width} solid ${vars.state.focus}`,
      outlineOffset: vars.focusRing.offset
    },
    '&:hover': {
      background: `color-mix(in srgb, ${vars.content.primary} 10%, transparent)`,
      color: vars.content.primary
    }
  },
  minHeight: vars.control.min,
  padding: '0 12px',
  borderRadius: '10px',
  background: `color-mix(in srgb, ${vars.content.primary} 5%, transparent)`,
  border: `1px solid ${vars.outline.variant}`,
  color: vars.content.secondary,
  fontFamily: 'inherit',
  fontSize: vars.typescale.labelLarge.size,
  lineHeight: vars.typescale.labelLarge.lineHeight,
  fontWeight: '500',
  display: 'inline-flex',
  alignItems: 'center',
  gap: '4px',
  cursor: 'pointer',
  transition: 'background-color 140ms ease, border-color 140ms ease, color 140ms ease'
})

export const actionBtn = style({
  selectors: {
    '&:focus-visible': {
      outline: `${vars.focusRing.width} solid ${vars.state.focus}`,
      outlineOffset: vars.focusRing.offset
    },
    '&:hover': {
      background: `color-mix(in srgb, ${vars.content.primary} 8%, transparent)`,
      color: vars.content.primary
    }
  },
  width: vars.control.min,
  height: vars.control.min,
  borderRadius: '10px',
  border: 'none',
  background: 'transparent',
  color: vars.content.secondary,
  display: 'inline-flex',
  alignItems: 'center',
  justifyContent: 'center',
  cursor: 'pointer',
  transition: 'background-color 120ms ease, color 120ms ease'
})

export const fabBtn = style({
  selectors: {
    '&:focus-visible': {
      outline: `${vars.focusRing.width} solid ${vars.state.focus}`,
      outlineOffset: vars.focusRing.offset
    },
    '&:hover': {
      background: `color-mix(in srgb, rgb(${vars.color.primary}) 88%, ${vars.content.primary})`
    }
  },
  width: vars.control.min,
  height: vars.control.min,
  borderRadius: '12px',
  border: 'none',
  background: `rgb(${vars.color.primary})`,
  color: `rgb(${vars.color.onPrimary})`,
  display: 'inline-flex',
  alignItems: 'center',
  justifyContent: 'center',
  cursor: 'pointer',
  boxShadow: `0 2px 6px rgba(${vars.color.primary}, 0.22)`
})

export const operationClose = style({
  selectors: {
    '&:focus-visible': {
      outline: `${vars.focusRing.width} solid ${vars.state.focus}`,
      outlineOffset: vars.focusRing.offset
    },
    '&:active': {
      transform: 'scale(0.96)'
    }
  },
  minHeight: vars.control.min,
  border: '0',
  padding: '4px 12px',
  borderRadius: '20px',
  color: `rgb(${vars.color.primary})`,
  background: 'transparent',
  cursor: 'pointer',
  font: 'inherit',
  transition: 'background-color 140ms ease, transform 100ms ease'
})

export const snackbar = style({
  position: 'fixed',
  left: '50%',
  bottom: '24px',
  transform: 'translateX(-50%)',
  zIndex: '120',
  maxWidth: 'calc(100vw - 32px)',
  boxSizing: 'border-box',
  padding: '12px 16px',
  borderRadius: vars.shape.cornerSmall,
  background: `rgb(${vars.color.inverseSurface})`,
  color: `rgb(${vars.color.inverseOnSurface})`,
  fontSize: vars.typescale.bodyMedium.size,
  boxShadow: '0 4px 16px rgba(0, 0, 0, 0.28)',
  animation: `${snackbarEnter} 200ms cubic-bezier(0.2, 0, 0, 1)`,
  selectors: {
    [`${appShellStyles.compact} &`]: {
      bottom: `max(calc(16px + ${vars.layout.navBarHeight} + env(safe-area-inset-bottom, 0px)), ${fallbackVar(trayStackTop, '0px')})`
    }
  }
})

export const toolbar = style({
  display: 'flex',
  flexWrap: 'wrap',
  flex: '0 0 auto',
  alignItems: 'center',
  justifyContent: 'space-between',
  gap: '8px 16px',
  width: '100%',
  minHeight: '52px',
  padding: `12px ${vars.layout.contentPad}`,
  boxSizing: 'border-box',
  borderBottom: 'none',
  background: 'transparent'
})

export const toolbarCompact = style({
  padding: '10px 16px',
  gap: '8px',
  vars: {
    [vars.layout.contentPad]: '16px'
  }
})

export const folderHeading = style({
  display: 'flex',
  flexWrap: 'wrap',
  flex: '1 1 auto',
  alignItems: 'center',
  gap: '8px 12px',
  minWidth: '0',
  maxWidth: '100%',
  selectors: {
    [`${toolbarCompact} &`]: {
      flexBasis: 'auto'
    }
  }
})

export const toolbarActions = style({
  display: 'flex',
  alignItems: 'center',
  justifyContent: 'flex-end',
  gap: '8px',
  minWidth: '0',
  maxWidth: '100%',
  flex: '0 0 auto',
  marginInlineStart: 'auto'
})

export const filterPillActive = style({
  background: vars.state.selection,
  borderColor: `color-mix(in srgb, ${vars.state.selectionContent} 35%, transparent)`,
  color: vars.state.selectionContent,
  fontWeight: '600'
})

export const actionBtnActive = style({
  selectors: {
    [`${actionBtn}&`]: {
      color: vars.state.selectionContent,
      background: vars.state.selection
    }
  }
})

export const operation = style({
  margin: `12px ${vars.layout.pagePad}`,
  padding: '16px',
  border: 'none',
  borderRadius: vars.shape.cornerMedium,
  background: `rgb(${vars.color.surfaceContainer})`,
  boxShadow: '0 4px 16px rgba(0, 0, 0, 0.12)',
  animation: `${fadeInUp} 200ms cubic-bezier(0.2, 0, 0, 1)`
})

export const operationHeading = style({
  display: 'flex',
  alignItems: 'center',
  justifyContent: 'space-between',
  gap: '12px'
})

export const operationTitle = style({
  margin: '0',
  fontSize: vars.typescale.titleMedium.size,
  fontWeight: vars.typescale.titleMedium.weight,
  lineHeight: vars.typescale.titleMedium.lineHeight
})

export const operationList = style({
  display: 'grid',
  gap: '8px',
  margin: '12px 0 0',
  padding: '0',
  listStyle: 'none'
})

export const operationItem = style({
  display: 'flex',
  flexWrap: 'wrap',
  gap: '4px 12px',
  justifyContent: 'space-between',
  overflowWrap: 'anywhere',
  fontSize: vars.typescale.bodySmall.size,
  lineHeight: vars.typescale.bodySmall.lineHeight
})

export const operationError = style({
  color: `rgb(${vars.color.error})`
})

export const externalBadge = style({
  display: 'inline-flex',
  alignItems: 'center',
  gap: '4px',
  minHeight: '24px',
  maxWidth: '100%',
  boxSizing: 'border-box',
  paddingInline: '8px',
  borderRadius: vars.shape.cornerFull,
  fontSize: vars.typescale.labelSmall.size,
  lineHeight: vars.typescale.labelSmall.lineHeight,
  overflowWrap: 'anywhere',
  transition: 'background-color 150ms ease, color 150ms ease, transform 120ms ease',
  flexShrink: '0',
  background: vars.state.warning,
  color: vars.state.warningContent
})

export const brokenBadge = style({
  display: 'inline-flex',
  alignItems: 'center',
  gap: '4px',
  minHeight: '24px',
  maxWidth: '100%',
  boxSizing: 'border-box',
  paddingInline: '8px',
  borderRadius: vars.shape.cornerFull,
  fontSize: vars.typescale.labelSmall.size,
  lineHeight: vars.typescale.labelSmall.lineHeight,
  overflowWrap: 'anywhere',
  transition: 'background-color 150ms ease, color 150ms ease, transform 120ms ease',
  flexShrink: '0',
  background: vars.state.error,
  color: vars.state.errorContent
})

export const encryptedBadge = style({
  display: 'inline-flex',
  alignItems: 'center',
  gap: '4px',
  minHeight: '24px',
  maxWidth: '100%',
  boxSizing: 'border-box',
  paddingInline: '8px',
  borderRadius: vars.shape.cornerFull,
  fontSize: vars.typescale.labelSmall.size,
  lineHeight: vars.typescale.labelSmall.lineHeight,
  overflowWrap: 'anywhere',
  transition: 'background-color 150ms ease, color 150ms ease, transform 120ms ease',
  flexShrink: '0',
  background: `rgb(${vars.color.surfaceContainerHighest})`,
  color: vars.content.secondary
})

export const encryptedBadgeLocked = style({
  minHeight: vars.control.min,
  paddingInline: '12px',
  border: 'none',
  cursor: 'pointer',
  background: `rgb(${vars.color.secondaryContainer})`,
  color: `rgb(${vars.color.onSecondaryContainer})`,
  selectors: {
    '&:active': {
      transform: 'scale(0.95)'
    },
    '&:focus-visible': {
      outline: `${vars.focusRing.width} solid ${vars.state.focus}`,
      outlineOffset: vars.focusRing.offset
    }
  }
})

export const tableWrapMarquee = style({
  userSelect: 'none',
  cursor: 'crosshair'
})

export const marquee = style({
  position: 'fixed',
  zIndex: '15',
  pointerEvents: 'none',
  border: `1px solid ${vars.state.focus}`,
  background: `rgba(${vars.color.primary}, 0.18)`,
  borderRadius: '2px'
})

export const content = style({
  display: 'flex',
  flex: '1',
  minHeight: '0',
  containerType: 'inline-size',
  containerName: hostContainer
})

export const tableWrap = style({
  position: 'relative',
  flex: '1',
  display: 'flex',
  minWidth: '0',
  minHeight: '0'
})

export const tableWrapDragover = style({
  outline: `2px dashed rgb(${vars.color.primary})`,
  outlineOffset: '-2px'
})

export const view = style({
  position: 'absolute',
  inset: '0',
  display: 'flex',
  minWidth: '0',
  minHeight: '0'
})

export const loading = style({
  display: 'flex',
  alignItems: 'center',
  justifyContent: 'center',
  flex: '1',
  minHeight: '240px'
})

export const loadingMore = style({
  display: 'flex',
  alignItems: 'center',
  justifyContent: 'center',
  gap: '8px',
  padding: '12px',
  color: vars.content.secondary
})

export const nothing = style({
  display: 'flex',
  flex: '1',
  flexDirection: 'column',
  alignItems: 'center',
  justifyContent: 'center',
  gap: '16px',
  padding: '48px 24px',
  textAlign: 'center',
  animation: `${fadeInUp} 220ms cubic-bezier(0.2,0,0,1)`
})

export const nothingIcon = style({
  width: '72px',
  height: '72px',
  display: 'inline-flex',
  alignItems: 'center',
  justifyContent: 'center',
  borderRadius: '24px',
  background: vars.state.selection,
  color: vars.state.selectionContent,
  fontSize: '36px',
  lineHeight: '1',
  boxShadow: vars.shadow.card
})

export const nothingTitle = style({
  margin: '0',
  fontSize: vars.typescale.headlineSmall.size,
  fontWeight: vars.typescale.headlineSmall.weight,
  lineHeight: vars.typescale.headlineSmall.lineHeight
})

export const nothingHint = style({
  maxWidth: '360px',
  margin: '0',
  color: vars.content.secondary,
  fontSize: vars.typescale.bodyMedium.size,
  lineHeight: vars.typescale.bodyMedium.lineHeight
})

export const error = style({
  padding: '24px',
  color: `rgb(${vars.color.error})`
})

export const dropOverlay = style({
  position: 'absolute',
  inset: '0',
  display: 'flex',
  alignItems: 'center',
  justifyContent: 'center',
  background: `rgba(${vars.color.primary}, 0.12)`,
  color: `rgb(${vars.color.primary})`,
  fontSize: vars.typescale.titleMedium.size,
  fontWeight: vars.typescale.titleMedium.weight,
  pointerEvents: 'none'
})

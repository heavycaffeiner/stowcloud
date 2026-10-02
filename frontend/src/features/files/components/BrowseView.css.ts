import { style } from '@vanilla-extract/css'
import * as appShellStyles from '../../../app/shell/AppShell.css'
import { hostContainer } from './FileTree.css'
import { compactFloatBottom, fadeInUp, vars } from '@/shared/theme'

// The notice sits above the compact layout's navigation bar.
export const snackbar = style({
  selectors: { [`${appShellStyles.compact} &`]: { insetBlockEnd: compactFloatBottom } }
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

export const operation = style({
  margin: `12px ${vars.layout.pagePad}`,
  padding: '16px',
  border: 'none',
  borderRadius: vars.radius.md,
  background: vars.color.surface.raised,
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
  fontSize: vars.typography.title.size,
  fontWeight: vars.typography.title.weight,
  lineHeight: vars.typography.title.lineHeight
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
  fontSize: vars.typography.bodySmall.size,
  lineHeight: vars.typography.bodySmall.lineHeight
})

export const operationError = style({
  color: vars.color.danger.solid
})

export const tableWrapMarquee = style({
  userSelect: 'none',
  cursor: 'crosshair'
})

export const marquee = style({
  position: 'fixed',
  zIndex: '15',
  pointerEvents: 'none',
  border: `1px solid ${vars.color.focus}`,
  background: `color-mix(in srgb, ${vars.color.accent.solid} 18%, transparent)`,
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
  outline: `2px dashed ${vars.color.accent.solid}`,
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
  color: vars.color.text.secondary
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
  background: vars.color.selection.bg,
  color: vars.color.selection.fg,
  fontSize: '36px',
  lineHeight: '1',
  boxShadow: vars.elevation.sm
})

export const nothingTitle = style({
  margin: '0',
  fontSize: vars.typography.heading.size,
  fontWeight: vars.typography.heading.weight,
  lineHeight: vars.typography.heading.lineHeight
})

export const nothingHint = style({
  maxWidth: '360px',
  margin: '0',
  color: vars.color.text.secondary,
  fontSize: vars.typography.body.size,
  lineHeight: vars.typography.body.lineHeight
})

export const error = style({
  padding: '24px',
  color: vars.color.danger.solid
})

export const dropOverlay = style({
  position: 'absolute',
  inset: '0',
  display: 'flex',
  alignItems: 'center',
  justifyContent: 'center',
  background: `color-mix(in srgb, ${vars.color.accent.solid} 12%, transparent)`,
  color: vars.color.accent.solid,
  fontSize: vars.typography.title.size,
  fontWeight: vars.typography.title.weight,
  pointerEvents: 'none'
})

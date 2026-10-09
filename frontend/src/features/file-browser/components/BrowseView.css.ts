import { style } from '@vanilla-extract/css'
import * as appShellStyles from '../../../app/shell/AppShell.css'
import { hostContainer } from './FolderTree/FolderTree.css'
import { compactFloatBottom, fadeInUp, typography, vars } from '@/shared/theme'

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
  gap: `${vars.space.sm} ${vars.space.lg}`,
  width: '100%',
  padding: `${vars.space.md} ${vars.layout.contentPad}`,
  boxSizing: 'border-box',
  borderBottom: 'none',
  background: 'transparent'
})

// Applied below the wide breakpoint, which reaches past the compact media query that also narrows the padding.
export const toolbarCompact = style({
  paddingBlock: vars.space.sm,
  gap: vars.space.sm,
  vars: { [vars.layout.contentPad]: vars.space.lg }
})

export const folderHeading = style({
  display: 'flex',
  flexWrap: 'wrap',
  flex: '1 1 auto',
  alignItems: 'center',
  gap: `${vars.space.sm} ${vars.space.md}`,
  minWidth: 0,
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
  gap: vars.space.sm,
  minWidth: 0,
  maxWidth: '100%',
  flex: '0 0 auto',
  marginInlineStart: 'auto'
})

export const operation = style({
  margin: `${vars.space.md} ${vars.layout.pagePad}`,
  padding: vars.space.lg,
  border: 'none',
  borderRadius: vars.radius.md,
  background: vars.color.surface.raised,
  boxShadow: vars.elevation.lg,
  animation: `${fadeInUp} ${vars.motion.medium} ${vars.motion.easing}`
})

export const operationHeading = style({
  display: 'flex',
  alignItems: 'center',
  justifyContent: 'space-between',
  gap: vars.space.md
})

export const operationTitle = style({
  margin: 0,
  ...typography('title')
})

export const operationList = style({
  display: 'grid',
  gap: vars.space.sm,
  margin: `${vars.space.md} 0 0`,
  padding: 0,
  listStyle: 'none'
})

export const operationItem = style({
  display: 'flex',
  flexWrap: 'wrap',
  gap: `${vars.space.xs} ${vars.space.md}`,
  justifyContent: 'space-between',
  overflowWrap: 'anywhere',
  ...typography('bodySmall')
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
  zIndex: 15,
  pointerEvents: 'none',
  border: `${vars.stroke.thin} solid ${vars.color.focus}`,
  background: `color-mix(in srgb, ${vars.color.accent.solid} 18%, transparent)`,
  borderRadius: vars.radius.xs
})

export const content = style({
  display: 'flex',
  flex: 1,
  minHeight: 0,
  containerType: 'inline-size',
  containerName: hostContainer
})

export const tableWrap = style({
  position: 'relative',
  flex: 1,
  display: 'flex',
  flexDirection: 'column',
  minWidth: 0,
  minHeight: 0
})

export const tableWrapDragover = style({
  outline: `${vars.stroke.thick} dashed ${vars.color.accent.solid}`,
  outlineOffset: `calc(-1 * ${vars.stroke.thick})`
})

export const view = style({
  display: 'flex',
  flex: 1,
  minWidth: 0,
  minHeight: 0
})

export const parentFolder = style({
  paddingInline: vars.layout.contentPad,
  borderBottom: `${vars.stroke.thin} solid ${vars.color.border.subtle}`
})

export const loading = style({
  display: 'flex',
  alignItems: 'center',
  justifyContent: 'center',
  flex: 1,
  minHeight: '240px'
})

export const loadingMore = style({
  display: 'flex',
  alignItems: 'center',
  justifyContent: 'center',
  gap: vars.space.sm,
  padding: vars.space.md,
  color: vars.color.text.secondary
})

export const nothing = style({
  display: 'flex',
  flex: 1,
  flexDirection: 'column',
  alignItems: 'center',
  justifyContent: 'center',
  gap: vars.space.lg,
  padding: `${vars.space.xxl} ${vars.space.xl}`,
  textAlign: 'center',
  animation: `${fadeInUp} ${vars.motion.medium} ${vars.motion.easing}`
})

export const nothingIcon = style({
  width: '72px',
  aspectRatio: '1',
  display: 'inline-flex',
  alignItems: 'center',
  justifyContent: 'center',
  borderRadius: vars.radius.xl,
  background: vars.color.selection.bg,
  color: vars.color.selection.fg,
  boxShadow: vars.elevation.sm
})

export const nothingTitle = style({
  margin: 0,
  ...typography('heading')
})

export const nothingHint = style({
  maxWidth: '360px',
  margin: 0,
  color: vars.color.text.secondary,
  ...typography('body')
})

export const error = style({
  padding: vars.space.xl,
  color: vars.color.danger.solid
})

export const dropOverlay = style({
  position: 'absolute',
  inset: 0,
  display: 'flex',
  alignItems: 'center',
  justifyContent: 'center',
  background: `color-mix(in srgb, ${vars.color.accent.solid} 12%, transparent)`,
  color: vars.color.accent.solid,
  ...typography('title'),
  pointerEvents: 'none'
})

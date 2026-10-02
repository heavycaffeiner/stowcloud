import { createContainer, style } from '@vanilla-extract/css'
import { figures as adminFigures } from './admin.css'
import { focusOutline, media, typography, vars } from '@/shared/theme'

const logs = createContainer()
const narrow = `${logs} ${media.compact}`

export const root = style({
  minWidth: 0,
  containerName: logs,
  containerType: 'inline-size'
})

export const title = style({
  margin: `0 0 ${vars.space.sm}`,
  ...typography('titleLarge'),
  color: vars.color.text.primary
})

export const timelineTitle = style({
  margin: 0,
  ...typography('title')
})

export const chartHead = style({
  marginBottom: vars.space.lg
})

export const hint = style({
  maxWidth: vars.layout.measure,
  margin: `0 0 ${vars.space.lg}`,
  color: vars.color.text.secondary,
  ...typography('bodySmall'),
  selectors: {
    [`${chartHead} &`]: {
      margin: `${vars.space.xs} 0 0`
    }
  }
})

export const figures = style([adminFigures, { maxWidth: '30rem', marginBottom: vars.space.xl }])

export const filters = style({
  display: 'flex',
  flexDirection: 'column',
  alignItems: 'flex-start',
  gap: vars.space.lg,
  marginBottom: vars.space.xl
})

export const sources = style({
  display: 'flex',
  flexDirection: 'column',
  gap: vars.space.sm
})

export const groupLabel = style({
  color: vars.color.text.secondary,
  ...typography('bodySmall')
})

export const levelsLegend = style({
  color: vars.color.text.secondary,
  ...typography('bodySmall'),
  padding: 0,
  marginBottom: vars.space.sm
})

export const sourceGroup = style({
  display: 'flex',
  flexWrap: 'wrap',
  gap: vars.space.xs
})

export const bar = style({
  selectors: {
    '&:focus-visible': focusOutline
  },
  display: 'flex',
  alignItems: 'flex-end',
  flex: '1 1 0',
  minInlineSize: 0,
  blockSize: '100%',
  padding: 0,
  border: 0,
  borderRadius: vars.radius.xs,
  background: 'none',
  cursor: 'pointer'
})

export const rowButton = style({
  selectors: {
    '&:focus-visible': focusOutline
  },
  background: 'none',
  border: 0,
  font: 'inherit',
  cursor: 'pointer'
})

export const tableSummary = style({
  selectors: {
    '&:focus-visible': focusOutline
  },
  display: 'flex',
  alignItems: 'center',
  width: 'fit-content',
  minBlockSize: vars.density.control,
  padding: `${vars.space.xs} 0`,
  color: vars.color.accent.solid,
  cursor: 'pointer',
  ...typography('bodySmall'),
  '@container': {
    [narrow]: {
      minBlockSize: vars.density.controlCompact
    }
  }
})

export const levels = style({
  margin: 0,
  padding: 0,
  border: 0
})

export const levelBoxes = style({
  display: 'flex',
  flexWrap: 'wrap',
  gap: vars.space.lg
})

export const fields = style({
  display: 'flex',
  flexWrap: 'wrap',
  alignItems: 'center',
  gap: vars.space.lg,
  width: '100%',
  '@container': {
    [narrow]: {
      flexDirection: 'column',
      alignItems: 'stretch'
    }
  }
})

export const field = style({
  flex: '1 1 180px',
  maxWidth: '280px',
  '@container': {
    [narrow]: {
      flex: 'none',
      width: '100%',
      maxWidth: 'none'
    }
  }
})

export const autoNote = style({
  margin: 0,
  color: vars.color.text.secondary,
  ...typography('bodySmall')
})

export const scope = style({
  margin: 0,
  color: vars.color.text.secondary,
  ...typography('bodySmall'),
  display: 'flex',
  alignItems: 'flex-start',
  gap: vars.space.sm,
  maxWidth: vars.layout.measure
})

export const warn = style({
  margin: 0,
  ...typography('bodySmall'),
  display: 'flex',
  alignItems: 'flex-start',
  gap: vars.space.sm,
  maxWidth: vars.layout.measure,
  padding: `${vars.space.sm} ${vars.space.md}`,
  borderRadius: vars.radius.xs,
  background: vars.color.highlight.soft,
  color: vars.color.highlight.onSoft
})

export const chart = style({
  marginBottom: vars.space.xl
})

export const legend = style({
  display: 'flex',
  flexWrap: 'wrap',
  gap: `${vars.space.sm} ${vars.space.lg}`,
  margin: `0 0 ${vars.space.md}`,
  padding: 0,
  listStyle: 'none'
})

export const legendItem = style({
  display: 'inline-flex',
  alignItems: 'center',
  gap: vars.space.xs,
  color: vars.color.text.primary,
  ...typography('labelSmall')
})

export const swatch = style({
  display: 'inline-block',
  inlineSize: vars.space.lg,
  blockSize: vars.space.lg,
  border: 'none',
  borderRadius: vars.radius.xs,
  background: vars.color.border.strong
})

export const plot = style({
  display: 'flex',
  alignItems: 'flex-end',
  gap: vars.space.xs,
  blockSize: '160px',
  minWidth: 0,
  padding: vars.space.sm,
  border: 'none',
  borderRadius: vars.radius.md,
  background: vars.color.surface.sunken,
  boxShadow: `inset 0 ${vars.stroke.thin} 3px color-mix(in srgb, ${vars.color.shadow} 20%, transparent)`,
  overflow: 'hidden',
  '@container': {
    [narrow]: {
      gap: vars.space.xxs
    }
  }
})

export const barActive = style({
  background: vars.color.surface.overlay,
  outline: `${vars.stroke.thin} solid ${vars.color.border.strong}`
})

export const stack = style({
  display: 'flex',
  flexDirection: 'column-reverse',
  justifyContent: 'flex-start',
  inlineSize: '100%',
  blockSize: '100%'
})

export const seg = style({
  flexShrink: 0,
  inlineSize: '100%',
  minBlockSize: vars.stroke.thin,
  background: vars.color.border.strong
})

export const baseline = style({
  inlineSize: '100%',
  blockSize: vars.stroke.thick,
  background: vars.color.border.subtle
})

export const segServerDebug = style({
  background: vars.color.surface.fill
})

export const segServerInfo = style({
  background: vars.color.selection.bg,
  backgroundImage: `repeating-linear-gradient(45deg, transparent 0 3px, ${vars.color.selection.fg} 3px 4px)`
})

export const segServerWarn = style({
  background: vars.color.highlight.soft,
  backgroundImage: `repeating-linear-gradient(-45deg, transparent 0 3px, ${vars.color.highlight.onSoft} 3px 4px)`
})

export const segServerError = style({
  background: vars.color.danger.soft,
  backgroundImage: `repeating-linear-gradient(90deg, transparent 0 2px, ${vars.color.danger.onSoft} 2px 4px)`
})

export const segAuditOk = style({
  background: vars.color.accent.soft,
  backgroundImage: `repeating-linear-gradient(0deg, transparent 0 3px, ${vars.color.accent.onSoft} 3px 4px)`
})

export const segAuditFailed = style({
  background: vars.color.danger.soft,
  backgroundImage: `repeating-linear-gradient(45deg, transparent 0 3px, ${vars.color.danger.onSoft} 3px 4px), repeating-linear-gradient(-45deg, transparent 0 3px, ${vars.color.danger.onSoft} 3px 4px)`
})

export const axis = style({
  display: 'flex',
  justifyContent: 'space-between',
  gap: vars.space.sm,
  marginTop: vars.space.sm,
  color: vars.color.text.secondary,
  ...typography('bodySmall')
})

export const readout = style({
  margin: `${vars.space.sm} 0 0`,
  color: vars.color.text.primary,
  ...typography('bodySmall')
})

export const tableWrap = style({
  minWidth: 0,
  marginTop: vars.space.md
})

export const tableScroll = style({
  maxWidth: '100%',
  maxHeight: '320px',
  overflow: 'auto',
  marginTop: vars.space.sm,
  border: 'none',
  background: vars.color.surface.container,
  borderRadius: vars.radius.xs
})

export const table = style({
  width: '100%',
  minWidth: '28rem',
  borderCollapse: 'collapse',
  ...typography('bodySmall')
})

export const tableCaption = style({
  padding: vars.space.sm,
  textAlign: 'start',
  color: vars.color.text.secondary
})

export const tableCell = style({
  padding: `${vars.space.xs} ${vars.space.sm}`,
  textAlign: 'start',
  whiteSpace: 'nowrap',
  borderTop: 'none'
})

export const tableCol = style({
  position: 'sticky',
  top: 0,
  background: vars.color.surface.container
})

export const tableNum = style({
  textAlign: 'end'
})

export const error = style({
  margin: `${vars.space.sm} 0 0`,
  color: vars.color.danger.solid,
  ...typography('bodySmall')
})

export const note = style({
  margin: `${vars.space.lg} 0 0`,
  textAlign: 'center',
  color: vars.color.text.secondary,
  ...typography('bodySmall')
})

export const empty = style({
  display: 'flex',
  flexDirection: 'column',
  alignItems: 'center',
  gap: vars.space.sm,
  padding: `${vars.space.xxl} ${vars.space.lg}`,
  color: vars.color.text.secondary,
  textAlign: 'center',
  border: `${vars.stroke.thin} dashed ${vars.color.border.subtle}`,
  borderRadius: vars.radius.md,
  background: 'transparent'
})

export const emptyText = style({
  margin: 0
})

export const emptyHint = style({
  ...typography('bodySmall')
})

export const list = style({
  listStyle: 'none',
  margin: 0,
  padding: 0,
  border: `${vars.stroke.thin} solid ${vars.color.border.subtle}`,
  borderRadius: vars.radius.md,
  background: 'transparent',
  display: 'flex',
  flexDirection: 'column',
  gap: 0,
  overflow: 'hidden'
})

export const item = style({
  background: 'transparent',
  borderRadius: 0,
  overflow: 'hidden',
  selectors: {
    '& + &': {
      borderTop: `${vars.stroke.thin} solid ${vars.color.border.subtle}`
    }
  }
})

export const row = style({
  display: 'flex',
  flexWrap: 'wrap',
  alignItems: 'center',
  gap: vars.space.lg,
  width: '100%',
  minWidth: 0,
  minHeight: vars.density.row,
  boxSizing: 'border-box',
  padding: `${vars.space.sm} ${vars.space.lg}`,
  color: vars.color.text.primary,
  textAlign: 'start',
  '@container': {
    [narrow]: {
      alignItems: 'flex-start',
      gap: `${vars.space.sm} ${vars.space.md}`,
      paddingInline: vars.space.md
    }
  }
})

// Wide enough for the longest level label, so the messages line up.
const levelWidth = '5.5rem'

export const level = style({
  minInlineSize: levelWidth
})

export const body = style({
  display: 'flex',
  flexDirection: 'column',
  gap: vars.space.xs,
  minWidth: 0,
  flex: '1 0 12rem',
  '@container': {
    [narrow]: {
      flexBasis: `calc(100% - ${levelWidth} - ${vars.space.md})`
    }
  }
})

export const msg = style({
  ...typography('body'),
  overflowWrap: 'anywhere'
})

export const meta = style({
  display: 'flex',
  flexWrap: 'wrap',
  gap: vars.space.sm,
  color: vars.color.text.secondary,
  ...typography('bodySmall'),
  overflowWrap: 'anywhere'
})

export const source = style({
  paddingInline: vars.space.xs,
  border: 'none',
  background: vars.color.surface.fill,
  borderRadius: vars.radius.xs
})

export const disclose = style({
  display: 'inline-flex',
  alignItems: 'center',
  gap: vars.space.xs,
  flexShrink: 0,
  marginInlineStart: 'auto',
  color: vars.color.text.secondary,
  ...typography('bodySmall'),
  '@container': {
    [narrow]: {
      width: '100%',
      justifyContent: 'flex-end'
    }
  }
})

export const attrs = style({
  display: 'grid',
  gridTemplateColumns: 'repeat(auto-fit, minmax(180px, 1fr))',
  gap: vars.space.sm,
  margin: 0,
  padding: vars.space.lg,
  background: vars.color.surface.container,
  '@container': {
    [narrow]: {
      gridTemplateColumns: 'minmax(0, 1fr)'
    }
  }
})

export const attr = style({
  display: 'flex',
  flexDirection: 'column',
  gap: vars.space.xs,
  minWidth: 0
})

export const attrLabel = style({
  color: vars.color.text.secondary,
  ...typography('bodySmall')
})

export const attrValue = style({
  margin: 0,
  overflowWrap: 'anywhere',
  ...typography('bodySmall')
})

export const more = style({
  display: 'flex',
  justifyContent: 'center',
  marginTop: vars.space.lg
})

import { createContainer, style } from '@vanilla-extract/css'
import { vars } from '../../../ui/theme.css'

const logs = createContainer()

export const root = style({
  minWidth: '0',
  containerName: logs,
  containerType: 'inline-size'
})

export const title = style({
  margin: '0 0 8px',
  fontSize: vars.typescale.titleLarge.size,
  fontWeight: vars.typescale.titleLarge.weight,
  lineHeight: vars.typescale.titleLarge.lineHeight,
  color: `rgb(${vars.color.onSurface})`,
  letterSpacing: vars.typescale.titleLarge.tracking
})

export const timelineTitle = style({
  margin: '0',
  fontSize: vars.typescale.titleMedium.size,
  fontWeight: vars.typescale.titleMedium.weight,
  lineHeight: vars.typescale.titleMedium.lineHeight
})

export const chartHead = style({
  marginBottom: '16px'
})

export const hint = style({
  maxWidth: '40rem',
  margin: '0 0 16px',
  color: `rgb(${vars.color.onSurfaceVariant})`,
  fontSize: vars.typescale.bodySmall.size,
  lineHeight: vars.typescale.bodySmall.lineHeight,
  selectors: {
    [`${chartHead} &`]: {
      margin: '4px 0 0'
    }
  }
})

export const figures = style({
  display: 'grid',
  gridTemplateColumns: 'repeat(auto-fit, minmax(140px, 1fr))',
  gap: '16px',
  maxWidth: '30rem',
  margin: '0 0 24px'
})

export const figure = style({
  display: 'flex',
  flexDirection: 'column',
  gap: '4px'
})

export const figureLabel = style({
  color: `rgb(${vars.color.onSurfaceVariant})`,
  fontSize: vars.typescale.bodySmall.size
})

export const figureValue = style({
  margin: '0',
  color: `rgb(${vars.color.onSurface})`,
  fontSize: vars.typescale.titleMedium.size,
  lineHeight: vars.typescale.titleMedium.lineHeight
})

export const filters = style({
  display: 'flex',
  flexDirection: 'column',
  alignItems: 'flex-start',
  gap: '16px',
  marginBottom: '24px'
})

export const sources = style({
  display: 'flex',
  flexDirection: 'column',
  gap: '8px'
})

export const groupLabel = style({
  color: `rgb(${vars.color.onSurfaceVariant})`,
  fontSize: vars.typescale.bodySmall.size
})

export const levelsLegend = style({
  color: `rgb(${vars.color.onSurfaceVariant})`,
  fontSize: vars.typescale.bodySmall.size,
  padding: '0',
  marginBottom: '8px'
})

export const sourceButton = style({
  minBlockSize: vars.control.min,
  paddingInline: '12px',
  border: 'none',
  borderRadius: vars.shape.cornerFull,
  background: `rgb(${vars.color.surfaceContainerHigh})`,
  color: 'inherit',
  cursor: 'pointer',
  transition: 'background-color 120ms ease, color 120ms ease',
  selectors: {
    '&:hover': {
      background: `rgb(${vars.color.surfaceContainerHighest})`
    },
    '&:focus-visible': {
      outline: `${vars.focusRing.width} solid ${vars.state.focus}`,
      outlineOffset: vars.focusRing.offset
    }
  },
  '@container': {
    [`${logs} (max-width: 599.98px)`]: {
      minBlockSize: '44px'
    }
  }
})

export const sourceButtonActive = style({
  background: `rgb(${vars.color.primaryContainer})`,
  color: `rgb(${vars.color.onPrimaryContainer})`
})

export const bar = style({
  selectors: {
    '&:focus-visible': {
      outline: `${vars.focusRing.width} solid ${vars.state.focus}`,
      outlineOffset: vars.focusRing.offset
    }
  },
  display: 'flex',
  alignItems: 'flex-end',
  flex: '1 1 0',
  minInlineSize: '0',
  blockSize: '100%',
  padding: '0',
  border: '0',
  borderRadius: '2px',
  background: 'none',
  cursor: 'pointer'
})

export const rowButton = style({
  selectors: {
    '&:focus-visible': {
      outline: `${vars.focusRing.width} solid ${vars.state.focus}`,
      outlineOffset: vars.focusRing.offset
    }
  },
  background: 'none',
  border: '0',
  font: 'inherit',
  cursor: 'pointer'
})

export const tableSummary = style({
  selectors: {
    '&:focus-visible': {
      outline: `${vars.focusRing.width} solid ${vars.state.focus}`,
      outlineOffset: vars.focusRing.offset
    }
  },
  display: 'flex',
  alignItems: 'center',
  width: 'fit-content',
  minBlockSize: vars.control.min,
  padding: '4px 0',
  color: `rgb(${vars.color.primary})`,
  cursor: 'pointer',
  fontSize: vars.typescale.bodySmall.size,
  '@container': {
    [`${logs} (max-width: 599.98px)`]: {
      minBlockSize: '44px'
    }
  }
})

export const levels = style({
  margin: '0',
  padding: '0',
  border: '0'
})

export const levelBoxes = style({
  display: 'flex',
  flexWrap: 'wrap',
  gap: '16px'
})

export const fields = style({
  display: 'flex',
  flexWrap: 'wrap',
  alignItems: 'center',
  gap: '16px',
  width: '100%',
  '@container': {
    [`${logs} (max-width: 599.98px)`]: {
      flexDirection: 'column',
      alignItems: 'stretch'
    }
  }
})

export const field = style({
  flex: '1 1 180px',
  maxWidth: '280px',
  '@container': {
    [`${logs} (max-width: 599.98px)`]: {
      flex: 'none',
      width: '100%',
      maxWidth: 'none'
    }
  }
})

export const autoNote = style({
  margin: '0',
  color: `rgb(${vars.color.onSurfaceVariant})`,
  fontSize: vars.typescale.bodySmall.size
})

export const scope = style({
  margin: '0',
  color: `rgb(${vars.color.onSurfaceVariant})`,
  fontSize: vars.typescale.bodySmall.size,
  display: 'flex',
  alignItems: 'flex-start',
  gap: '8px',
  maxWidth: '40rem'
})

export const warn = style({
  margin: '0',
  fontSize: vars.typescale.bodySmall.size,
  display: 'flex',
  alignItems: 'flex-start',
  gap: '8px',
  maxWidth: '40rem',
  padding: '8px 12px',
  borderRadius: vars.shape.cornerExtraSmall,
  background: `rgb(${vars.color.tertiaryContainer})`,
  color: `rgb(${vars.color.onTertiaryContainer})`
})

export const chart = style({
  marginBottom: '24px'
})

export const legend = style({
  display: 'flex',
  flexWrap: 'wrap',
  gap: '8px 16px',
  margin: '0 0 12px',
  padding: '0',
  listStyle: 'none'
})

export const legendItem = style({
  display: 'inline-flex',
  alignItems: 'center',
  gap: '4px',
  color: `rgb(${vars.color.onSurface})`,
  fontSize: vars.typescale.labelMedium.size
})

export const swatch = style({
  display: 'inline-block',
  inlineSize: '16px',
  blockSize: '16px',
  border: 'none',
  borderRadius: vars.shape.cornerExtraSmall,
  background: `rgb(${vars.color.outline})`
})

export const plot = style({
  display: 'flex',
  alignItems: 'flex-end',
  gap: '4px',
  blockSize: '160px',
  minWidth: '0',
  padding: '8px',
  border: 'none',
  borderRadius: vars.shape.cornerMedium,
  background: `rgb(${vars.color.surfaceContainerLowest})`,
  boxShadow: 'inset 0 1px 3px rgba(0, 0, 0, 0.2)',
  overflow: 'hidden',
  '@container': {
    [`${logs} (max-width: 599.98px)`]: {
      gap: '2px'
    }
  }
})

export const barActive = style({
  background: `rgb(${vars.color.surfaceContainerHigh})`,
  outline: `1px solid rgb(${vars.color.outline})`
})

export const stack = style({
  display: 'flex',
  flexDirection: 'column-reverse',
  justifyContent: 'flex-start',
  inlineSize: '100%',
  blockSize: '100%'
})

export const seg = style({
  flexShrink: '0',
  inlineSize: '100%',
  minBlockSize: '1px',
  background: `rgb(${vars.color.outline})`
})

export const baseline = style({
  inlineSize: '100%',
  blockSize: '2px',
  background: `rgb(${vars.color.outlineVariant})`
})

export const segServerDebug = style({
  background: `rgb(${vars.color.surfaceContainerHighest})`
})

export const segServerInfo = style({
  background: `rgb(${vars.color.secondaryContainer})`,
  backgroundImage: `repeating-linear-gradient(45deg, transparent 0 3px, rgb(${vars.color.onSecondaryContainer}) 3px 4px)`
})

export const segServerWarn = style({
  background: `rgb(${vars.color.tertiaryContainer})`,
  backgroundImage: `repeating-linear-gradient(-45deg, transparent 0 3px, rgb(${vars.color.onTertiaryContainer}) 3px 4px)`
})

export const segServerError = style({
  background: `rgb(${vars.color.errorContainer})`,
  backgroundImage: `repeating-linear-gradient(90deg, transparent 0 2px, rgb(${vars.color.onErrorContainer}) 2px 4px)`
})

export const segAuditOk = style({
  background: `rgb(${vars.color.primaryContainer})`,
  backgroundImage: `repeating-linear-gradient(0deg, transparent 0 3px, rgb(${vars.color.onPrimaryContainer}) 3px 4px)`
})

export const segAuditFailed = style({
  background: `rgb(${vars.color.errorContainer})`,
  backgroundImage: `repeating-linear-gradient(45deg, transparent 0 3px, rgb(${vars.color.onErrorContainer}) 3px 4px), repeating-linear-gradient(-45deg, transparent 0 3px, rgb(${vars.color.onErrorContainer}) 3px 4px)`
})

export const axis = style({
  display: 'flex',
  justifyContent: 'space-between',
  gap: '8px',
  marginTop: '8px',
  color: `rgb(${vars.color.onSurfaceVariant})`,
  fontSize: vars.typescale.bodySmall.size
})

export const readout = style({
  margin: '8px 0 0',
  color: `rgb(${vars.color.onSurface})`,
  fontSize: vars.typescale.bodySmall.size
})

export const tableWrap = style({
  minWidth: '0',
  marginTop: '12px'
})

export const tableScroll = style({
  maxWidth: '100%',
  maxHeight: '320px',
  overflow: 'auto',
  marginTop: '8px',
  border: 'none',
  background: `rgb(${vars.color.surfaceContainerLow})`,
  borderRadius: vars.shape.cornerExtraSmall
})

export const table = style({
  width: '100%',
  minWidth: '28rem',
  borderCollapse: 'collapse',
  fontSize: vars.typescale.bodySmall.size
})

export const tableCaption = style({
  padding: '8px',
  textAlign: 'start',
  color: `rgb(${vars.color.onSurfaceVariant})`
})

export const tableCell = style({
  padding: '4px 8px',
  textAlign: 'start',
  whiteSpace: 'nowrap',
  borderTop: 'none'
})

export const tableCol = style({
  position: 'sticky',
  top: '0',
  background: `rgb(${vars.color.surfaceContainerLow})`
})

export const tableNum = style({
  textAlign: 'end'
})

export const error = style({
  margin: '8px 0 0',
  color: `rgb(${vars.color.error})`,
  fontSize: vars.typescale.bodySmall.size
})

export const note = style({
  margin: '16px 0 0',
  textAlign: 'center',
  color: `rgb(${vars.color.onSurfaceVariant})`,
  fontSize: vars.typescale.bodySmall.size
})

export const empty = style({
  display: 'flex',
  flexDirection: 'column',
  alignItems: 'center',
  gap: '8px',
  padding: '32px 16px',
  color: `rgb(${vars.color.onSurfaceVariant})`,
  textAlign: 'center',
  border: `1px dashed ${vars.outline.variant}`,
  borderRadius: vars.radius.medium,
  background: 'transparent'
})

export const emptyText = style({
  margin: '0'
})

export const emptyHint = style({
  fontSize: vars.typescale.bodySmall.size
})

export const list = style({
  listStyle: 'none',
  margin: '0',
  padding: '0',
  border: `1px solid ${vars.outline.variant}`,
  borderRadius: vars.radius.medium,
  background: 'transparent',
  display: 'flex',
  flexDirection: 'column',
  gap: '0',
  overflow: 'hidden'
})

export const item = style({
  background: 'transparent',
  borderRadius: '0',
  overflow: 'hidden',
  selectors: {
    '& + &': {
      borderTop: `1px solid ${vars.outline.variant}`
    }
  }
})

export const row = style({
  display: 'flex',
  flexWrap: 'wrap',
  alignItems: 'center',
  gap: '16px',
  width: '100%',
  minWidth: '0',
  minHeight: vars.layout.rowHeight,
  boxSizing: 'border-box',
  padding: '8px 16px',
  color: `rgb(${vars.color.onSurface})`,
  textAlign: 'start',
  '@container': {
    [`${logs} (max-width: 599.98px)`]: {
      alignItems: 'flex-start',
      gap: '8px 12px',
      paddingInline: '12px'
    }
  }
})

export const level = style({
  display: 'inline-flex',
  alignItems: 'center',
  flexShrink: '0',
  gap: '4px',
  minWidth: '88px',
  padding: '4px 8px',
  borderRadius: vars.shape.cornerExtraSmall,
  background: `rgb(${vars.color.surfaceContainerHighest})`,
  color: `rgb(${vars.color.onSurfaceVariant})`,
  fontSize: vars.typescale.labelMedium.size
})

export const levelError = style({
  background: `rgb(${vars.color.errorContainer})`,
  color: `rgb(${vars.color.onErrorContainer})`
})

export const levelFailed = style({
  background: `rgb(${vars.color.errorContainer})`,
  color: `rgb(${vars.color.onErrorContainer})`
})

export const levelWarn = style({
  background: `rgb(${vars.color.tertiaryContainer})`,
  color: `rgb(${vars.color.onTertiaryContainer})`
})

export const levelInfo = style({
  background: `rgb(${vars.color.secondaryContainer})`,
  color: `rgb(${vars.color.onSecondaryContainer})`
})

export const levelOk = style({
  background: `rgb(${vars.color.primaryContainer})`,
  color: `rgb(${vars.color.onPrimaryContainer})`
})

export const body = style({
  display: 'flex',
  flexDirection: 'column',
  gap: '4px',
  minWidth: '0',
  flex: '1 0 12rem',
  '@container': {
    [`${logs} (max-width: 599.98px)`]: {
      flexBasis: 'calc(100% - 100px)'
    }
  }
})

export const msg = style({
  fontSize: vars.typescale.bodyMedium.size,
  overflowWrap: 'anywhere'
})

export const meta = style({
  display: 'flex',
  flexWrap: 'wrap',
  gap: '8px',
  color: `rgb(${vars.color.onSurfaceVariant})`,
  fontSize: vars.typescale.bodySmall.size,
  overflowWrap: 'anywhere'
})

export const source = style({
  paddingInline: '4px',
  border: 'none',
  background: `rgb(${vars.color.surfaceContainerHighest})`,
  borderRadius: vars.shape.cornerExtraSmall
})

export const disclose = style({
  display: 'inline-flex',
  alignItems: 'center',
  gap: '4px',
  flexShrink: '0',
  marginInlineStart: 'auto',
  color: `rgb(${vars.color.onSurfaceVariant})`,
  fontSize: vars.typescale.bodySmall.size,
  '@container': {
    [`${logs} (max-width: 599.98px)`]: {
      width: '100%',
      justifyContent: 'flex-end'
    }
  }
})

export const attrs = style({
  display: 'grid',
  gridTemplateColumns: 'repeat(auto-fit, minmax(180px, 1fr))',
  gap: '8px',
  margin: '0',
  padding: '16px',
  background: `rgb(${vars.color.surfaceContainerLow})`,
  '@container': {
    [`${logs} (max-width: 599.98px)`]: {
      gridTemplateColumns: 'minmax(0, 1fr)'
    }
  }
})

export const attr = style({
  display: 'flex',
  flexDirection: 'column',
  gap: '4px',
  minWidth: '0'
})

export const attrLabel = style({
  color: `rgb(${vars.color.onSurfaceVariant})`,
  fontSize: vars.typescale.bodySmall.size
})

export const attrValue = style({
  margin: '0',
  overflowWrap: 'anywhere',
  fontSize: vars.typescale.bodySmall.size
})

export const more = style({
  display: 'flex',
  justifyContent: 'center',
  marginTop: '16px'
})

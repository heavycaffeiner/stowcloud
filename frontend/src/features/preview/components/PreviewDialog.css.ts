import { style } from '@vanilla-extract/css'
import { iconButtonSize } from '../../../ui/IconButton.css'
import { vars } from '../../../ui/theme.css'

export const root = style({
  display: 'flex',
  flex: '1',
  flexDirection: 'column',
  minWidth: '0',
  minHeight: '0',
  background: `rgb(${vars.color.surfaceContainer})`,
  color: `rgb(${vars.color.onSurface})`,
  vars: {
    [iconButtonSize]: '44px'
  }
})

export const bar = style({
  display: 'flex',
  flex: 'none',
  alignItems: 'center',
  gap: '8px',
  padding: '16px',
  background: `rgb(${vars.color.surfaceContainerHigh})`
})

export const iconButton = style({
  borderRadius: vars.shape.cornerFull,
  background: `rgb(${vars.color.surfaceContainerHighest})`
})

export const meta = style({
  display: 'flex',
  flex: '1',
  flexDirection: 'column',
  gap: '2px',
  minWidth: '0'
})

export const name = style({
  overflow: 'hidden',
  textOverflow: 'ellipsis',
  whiteSpace: 'nowrap',
  fontSize: vars.typescale.titleMedium.size,
  fontWeight: '500'
})

export const size = style({
  color: `rgb(${vars.color.onSurfaceVariant})`,
  fontSize: vars.typescale.bodySmall.size
})

export const body = style({
  display: 'flex',
  flex: '1',
  minWidth: '0',
  minHeight: '0',
  padding: '16px'
})

export const nav = style({
  display: 'flex',
  flex: 'none',
  justifyContent: 'space-between',
  gap: '8px',
  padding: '0 16px 16px'
})

export const stage = style({
  display: 'flex',
  flex: '1',
  alignItems: 'center',
  justifyContent: 'center',
  minWidth: '0',
  minHeight: '0',
  overflow: 'auto',
  borderRadius: vars.shape.cornerLarge,
  background: `rgb(${vars.color.surfaceContainerLow})`
})

export const videoContainer = style({
  display: 'flex',
  alignItems: 'center',
  justifyContent: 'center',
  width: '100%',
  height: '100%',
  overflow: 'hidden'
})

export const video = style({
  maxWidth: '100%',
  maxHeight: '100%',
  borderRadius: '8px',
  boxShadow: '0 4px 24px rgb(0 0 0 / 40%)'
})

export const image = style({
  maxWidth: '100%',
  maxHeight: '100%',
  objectFit: 'contain'
})

export const text = style({
  width: '100%',
  height: '100%',
  margin: '0',
  padding: '16px',
  boxSizing: 'border-box',
  overflow: 'auto',
  whiteSpace: 'pre-wrap',
  overflowWrap: 'anywhere',
  color: 'inherit',
  fontSize: vars.typescale.bodyMedium.size,
  lineHeight: '1.6'
})

export const card = style({
  display: 'flex',
  flexDirection: 'column',
  alignItems: 'center',
  gap: '8px',
  width: 'min(480px, 100%)',
  padding: '24px',
  boxSizing: 'border-box',
  borderRadius: vars.shape.cornerLarge,
  background: `rgb(${vars.color.surfaceContainer})`,
  color: 'inherit',
  textAlign: 'center'
})

export const cardTitle = style({
  margin: '0',
  fontSize: vars.typescale.titleMedium.size,
  fontWeight: vars.typescale.titleMedium.weight,
  lineHeight: vars.typescale.titleMedium.lineHeight
})

export const cardReason = style({
  margin: '0',
  color: `rgb(${vars.color.onSurfaceVariant})`
})

export const cardDetail = style({
  margin: '0',
  color: `rgb(${vars.color.onSurfaceVariant})`,
  overflowWrap: 'anywhere',
  fontFamily: 'ui-monospace, monospace',
  fontSize: '.8rem'
})

export const cardActions = style({
  display: 'flex',
  flexWrap: 'wrap',
  justifyContent: 'center',
  gap: '12px',
  marginTop: '16px'
})

export const archive = style({
  display: 'flex',
  flexDirection: 'column',
  gap: '8px',
  width: '100%',
  maxWidth: '720px',
  maxHeight: '100%',
  padding: '16px',
  boxSizing: 'border-box',
  overflow: 'auto',
  color: 'inherit'
})

export const archiveCount = style({
  margin: '0',
  fontSize: vars.typescale.bodySmall.size,
  lineHeight: vars.typescale.bodySmall.lineHeight
})

export const archiveEmpty = style({
  margin: '0',
  fontSize: vars.typescale.bodySmall.size,
  lineHeight: vars.typescale.bodySmall.lineHeight
})

export const archiveList = style({
  margin: '0',
  padding: '0',
  listStyle: 'none'
})

export const archiveRow = style({
  display: 'grid',
  gridTemplateColumns: 'auto 1fr auto auto',
  gap: '12px',
  alignItems: 'center',
  width: '100%',
  padding: '8px 0',
  border: '0',
  borderBottom: `1px solid rgb(${vars.color.outlineVariant})`,
  background: 'none',
  color: 'inherit',
  font: 'inherit',
  textAlign: 'start',
  selectors: {
    'button&': {
      cursor: 'pointer'
    },
    '&:hover': {
      background: `rgb(${vars.color.surfaceContainerHighest})`
    },
    '&:focus-visible': {
      outline: `${vars.focusRing.width} solid ${vars.state.focus}`,
      outlineOffset: vars.focusRing.offset
    }
  }
})

export const archiveRowUp = style({
  gridTemplateColumns: 'auto 1fr'
})

export const archiveName = style({
  minWidth: '0',
  overflow: 'hidden',
  textOverflow: 'ellipsis',
  whiteSpace: 'nowrap'
})

export const archiveSkipped = style({
  color: `rgb(${vars.color.error})`
})

export const crumbs = style({
  display: 'flex',
  flexWrap: 'wrap',
  alignItems: 'center',
  gap: '4px',
  fontSize: vars.typescale.bodySmall.size,
  lineHeight: vars.typescale.bodySmall.lineHeight
})

export const crumb = style({
  maxWidth: '240px',
  overflow: 'hidden',
  padding: '4px',
  border: '0',
  borderRadius: '4px',
  background: 'none',
  color: 'inherit',
  font: 'inherit',
  textOverflow: 'ellipsis',
  whiteSpace: 'nowrap',
  cursor: 'pointer',
  selectors: {
    '&:hover:not(:disabled)': {
      background: `rgb(${vars.color.surfaceContainerHighest})`
    },
    '&:disabled': {
      cursor: 'default',
      opacity: '.75'
    },
    '&:focus-visible': {
      outline: `${vars.focusRing.width} solid ${vars.state.focus}`,
      outlineOffset: vars.focusRing.offset
    }
  }
})

export const crumbSep = style({
  opacity: '.5'
})

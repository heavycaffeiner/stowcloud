import { style } from '@vanilla-extract/css'
import { vars } from '../../../ui/theme.css'

export const issued = style({
  padding: '16px',
  marginBottom: '16px',
  borderRadius: vars.shape.cornerMedium,
  background: `rgb(${vars.color.secondaryContainer})`,
  color: `rgb(${vars.color.onSecondaryContainer})`,
  '@media': {
    '(max-width: 599.98px)': {
      padding: '12px'
    }
  }
})

export const issuedNote = style({
  margin: '0 0 8px',
  overflowWrap: 'anywhere'
})

export const urlRow = style({
  display: 'flex',
  alignItems: 'center',
  gap: '8px',
  minWidth: '0'
})

export const url = style({
  display: 'block',
  flex: '1 1 auto',
  minWidth: '0',
  maxWidth: '100%',
  boxSizing: 'border-box',
  padding: '8px',
  border: '0',
  borderRadius: vars.shape.cornerExtraSmall,
  background: `rgb(${vars.color.surface})`,
  color: `rgb(${vars.color.onSurface})`,
  font: 'inherit',
  overflowWrap: 'anywhere',
  resize: 'vertical',
  userSelect: 'all',
  whiteSpace: 'pre-wrap',
  selectors: {
    '&:focus-visible': {
      outline: `${vars.focusRing.width} solid ${vars.state.focus}`,
      outlineOffset: vars.focusRing.offset
    }
  }
})

export const copyFeedback = style({
  margin: '8px 0 0',
  fontSize: vars.typescale.bodySmall.size
})

export const copyFeedbackError = style({
  color: `rgb(${vars.color.error})`,
  overflowWrap: 'anywhere'
})

export const error = style({
  color: `rgb(${vars.color.error})`,
  overflowWrap: 'anywhere'
})

export const loading = style({
  display: 'flex',
  justifyContent: 'center',
  padding: '24px'
})

export const empty = style({
  color: `rgb(${vars.color.onSurfaceVariant})`,
  overflowWrap: 'anywhere'
})

export const list = style({
  display: 'flex',
  flexDirection: 'column',
  gap: '8px',
  minWidth: '0',
  margin: '0 0 16px',
  padding: '0',
  listStyle: 'none'
})

export const item = style({
  minWidth: '0',
  padding: '12px',
  border: 'none',
  borderRadius: vars.shape.cornerMedium,
  background: `rgb(${vars.color.surfaceContainer})`,
  boxShadow: '0 2px 6px rgba(0, 0, 0, .08)',
  transition: 'background-color 140ms ease',
  selectors: {
    '&:hover': {
      background: `rgb(${vars.color.surfaceContainerHigh})`
    }
  },
  '@media': {
    '(max-width: 599.98px)': {
      padding: '12px'
    }
  }
})

export const itemRow = style({
  display: 'flex',
  alignItems: 'flex-start',
  gap: '12px',
  minWidth: '0',
  '@media': {
    '(max-width: 599.98px)': {
      flexWrap: 'wrap'
    }
  }
})

export const itemMain = style({
  display: 'flex',
  flex: '1 1 auto',
  flexDirection: 'column',
  minWidth: '0',
  '@media': {
    '(max-width: 599.98px)': {
      flexBasis: 'calc(100% - 36px)'
    }
  }
})

export const itemLabel = style({
  color: `rgb(${vars.color.onSurface})`,
  fontWeight: '500',
  overflowWrap: 'anywhere'
})

export const itemMeta = style({
  color: `rgb(${vars.color.onSurfaceVariant})`,
  fontSize: vars.typescale.bodySmall.size,
  overflowWrap: 'anywhere'
})

export const itemActions = style({
  display: 'flex',
  flex: '0 0 auto',
  alignItems: 'center',
  flexWrap: 'wrap',
  justifyContent: 'flex-end',
  '@media': {
    '(max-width: 599.98px)': {
      width: '100%',
      justifyContent: 'flex-end'
    }
  }
})

export const createForm = style({
  display: 'flex',
  flexDirection: 'column',
  gap: '12px',
  minWidth: '0',
  padding: '12px',
  borderRadius: vars.shape.cornerMedium,
  background: `rgb(${vars.color.surfaceContainerLow})`,
  '@media': {
    '(max-width: 599.98px)': {
      padding: '12px'
    }
  }
})

export const editForm = style({
  display: 'flex',
  flexDirection: 'column',
  gap: '12px',
  minWidth: '0',
  padding: '12px',
  borderRadius: vars.shape.cornerMedium,
  background: `rgb(${vars.color.surfaceContainerLow})`,
  '@media': {
    '(max-width: 599.98px)': {
      padding: '12px'
    }
  }
})

export const createTitle = style({
  margin: '0',
  fontSize: vars.typescale.titleMedium.size,
  fontWeight: vars.typescale.titleMedium.weight,
  lineHeight: vars.typescale.titleMedium.lineHeight
})

export const permRow = style({
  display: 'flex',
  alignItems: 'center',
  flexWrap: 'wrap',
  gap: '12px 16px'
})

export const hint = style({
  margin: '0',
  color: `rgb(${vars.color.onSurfaceVariant})`,
  fontSize: vars.typescale.bodySmall.size,
  overflowWrap: 'anywhere'
})

export const editActions = style({
  display: 'flex',
  alignItems: 'center',
  justifyContent: 'flex-end',
  flexWrap: 'wrap',
  gap: '8px'
})

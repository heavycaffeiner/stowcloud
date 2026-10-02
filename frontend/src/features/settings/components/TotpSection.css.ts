import { style } from '@vanilla-extract/css'
import { vars } from '../../../ui/theme.css'

export const copyFeedback = style({
  margin: '0',
  color: `rgb(${vars.color.error})`,
  overflowWrap: 'anywhere'
})

export const codes = style({
  display: 'grid',
  gridTemplateColumns: 'repeat(2, minmax(0, 1fr))',
  gap: '8px',
  listStyle: 'none',
  margin: '0',
  padding: '0',
  '@media': {
    '(max-width: 599.98px)': {
      gridTemplateColumns: '1fr'
    }
  }
})

export const code = style({
  width: '100%',
  boxSizing: 'border-box',
  padding: '8px 12px',
  minHeight: '40px',
  height: '40px',
  lineHeight: '24px',
  border: '0',
  borderRadius: vars.shape.cornerExtraSmall,
  background: `rgb(${vars.color.surfaceContainerHighest})`,
  color: `rgb(${vars.color.onSurface})`,
  font: 'inherit',
  overflowWrap: 'anywhere',
  selectors: {
    '&:focus-visible': {
      outline: `${vars.focusRing.width} solid ${vars.state.focus}`,
      outlineOffset: vars.focusRing.offset
    }
  }
})

export const secret = style({
  width: '100%',
  boxSizing: 'border-box',
  padding: '8px 12px',
  minHeight: '40px',
  height: '40px',
  lineHeight: '24px',
  border: '0',
  borderRadius: vars.shape.cornerExtraSmall,
  background: `rgb(${vars.color.surfaceContainerHighest})`,
  color: `rgb(${vars.color.onSurface})`,
  font: 'inherit',
  overflowWrap: 'anywhere',
  selectors: {
    '&:focus-visible': {
      outline: `${vars.focusRing.width} solid ${vars.state.focus}`,
      outlineOffset: vars.focusRing.offset
    }
  }
})

export const secretRow = style({
  display: 'flex',
  alignItems: 'center',
  gap: '8px',
  minWidth: '0',
  '@media': {
    '(max-width: 599.98px)': {
      alignItems: 'stretch'
    }
  }
})

export const root = style({
  display: 'flex',
  flexDirection: 'column',
  gap: '16px',
  minWidth: '0'
})

export const status = style({
  display: 'flex',
  alignItems: 'center',
  flexWrap: 'wrap',
  gap: '16px'
})

export const recovery = style({
  display: 'flex',
  alignItems: 'center',
  flexWrap: 'wrap',
  gap: '16px'
})

export const badge = style({
  display: 'inline-flex',
  alignItems: 'center',
  minBlockSize: '24px',
  paddingInline: '8px',
  borderRadius: vars.shape.cornerFull,
  background: `rgb(${vars.color.surfaceContainerHighest})`,
  color: `rgb(${vars.color.onSurfaceVariant})`,
  fontSize: vars.typescale.labelSmall.size,
  lineHeight: vars.typescale.labelSmall.lineHeight
})

export const badgeOn = style({
  background: `rgb(${vars.color.primaryContainer})`,
  color: `rgb(${vars.color.onPrimaryContainer})`
})

export const recoveryCount = style({
  margin: '0',
  color: `rgb(${vars.color.onSurfaceVariant})`,
  overflowWrap: 'anywhere'
})

export const recoveryCountLow = style({
  color: `rgb(${vars.color.error})`,
  fontWeight: '500'
})

export const smbWarning = style({
  display: 'flex',
  alignItems: 'flex-start',
  gap: '8px',
  margin: '0',
  padding: '12px',
  borderRadius: vars.shape.cornerExtraSmall,
  background: `rgb(${vars.color.errorContainer})`,
  color: `rgb(${vars.color.onErrorContainer})`,
  overflowWrap: 'anywhere'
})

export const url = style({
  margin: '0',
  color: `rgb(${vars.color.onSurfaceVariant})`,
  overflowWrap: 'anywhere'
})

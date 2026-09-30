import { style } from '@vanilla-extract/css'
import { vars } from '../../ui/theme.css'

export const adminSaved = style({
  margin: '0',
  color: `rgb(${vars.color.primary})`,
  fontSize: vars.typescale.bodySmall.size,
  overflowWrap: 'anywhere'
})

export const estimate = style({
  display: 'grid',
  gridTemplateColumns: 'repeat(auto-fit, minmax(140px, 1fr))',
  gap: '16px',
  margin: '0',
  padding: '16px',
  border: `1px solid ${vars.outline.variant}`,
  borderRadius: vars.radius.medium,
  background: 'transparent'
})

export const estimateLabel = style({
  color: `rgb(${vars.color.onSurfaceVariant})`,
  fontSize: vars.typescale.bodySmall.size,
  marginBottom: '4px'
})

export const estimateValue = style({
  margin: '0',
  fontSize: vars.typescale.titleMedium.size,
  fontWeight: '600',
  color: `rgb(${vars.color.onSurface})`
})

export const form = style({
  display: 'flex',
  alignItems: 'center',
  flexWrap: 'wrap',
  gap: '16px',
  maxWidth: '36rem',
  '@media': {
    '(max-width: 599.98px)': {
      alignItems: 'stretch',
      flexDirection: 'column'
    }
  }
})

export const field = style({
  flex: '1 1 180px',
  '@media': {
    '(max-width: 599.98px)': {
      flex: 'none'
    }
  }
})

export const actions = style({
  display: 'flex',
  alignItems: 'center',
  flexWrap: 'wrap',
  gap: '8px',
  flexShrink: '0',
  '@media': {
    '(max-width: 599.98px)': {
      flexWrap: 'wrap'
    }
  }
})

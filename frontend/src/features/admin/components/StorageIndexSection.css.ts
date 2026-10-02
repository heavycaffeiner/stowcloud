import { style } from '@vanilla-extract/css'
import { vars } from '../../../ui/theme.css'

export const list = style({
  listStyle: 'none',
  margin: '0',
  padding: '0',
  minWidth: '0',
  overflow: 'hidden',
  border: `1px solid ${vars.outline.variant}`,
  borderRadius: vars.radius.medium,
  background: 'transparent',
  display: 'flex',
  flexDirection: 'column',
  gap: '0'
})

export const item = style({
  minWidth: '0',
  background: 'transparent',
  borderRadius: '0',
  padding: '12px 16px',
  display: 'flex',
  alignItems: 'center',
  justifyContent: 'space-between',
  gap: '16px',
  transition: 'background-color 140ms ease',
  selectors: {
    '&:hover': {
      background: `rgb(${vars.color.surfaceContainerLow})`
    },
    '& + &': {
      borderTop: `1px solid ${vars.outline.variant}`
    }
  },
  '@media': {
    '(max-width: 599.98px)': {
      alignItems: 'flex-start',
      flexDirection: 'column',
      gap: '8px'
    }
  }
})

export const value = style({
  flexShrink: '0',
  color: `rgb(${vars.color.onSurfaceVariant})`,
  fontWeight: '500',
  fontSize: vars.typescale.bodyMedium.size,
  textAlign: 'end',
  overflowWrap: 'anywhere',
  '@media': {
    '(max-width: 599.98px)': {
      width: '100%',
      textAlign: 'start'
    }
  }
})

export const itemLabel = style({
  display: 'flex',
  alignItems: 'center',
  gap: '10px',
  minWidth: '0',
  fontWeight: '500',
  color: `rgb(${vars.color.onSurface})`
})

export const itemName = style({
  minWidth: '0',
  overflowWrap: 'anywhere'
})

export const statusRow = style({
  display: 'flex',
  alignItems: 'center',
  flexWrap: 'wrap',
  gap: '12px'
})

export const statusBadge = style({
  display: 'inline-flex',
  alignItems: 'center',
  gap: '6px',
  minHeight: '28px',
  padding: '2px 10px',
  borderRadius: vars.shape.cornerFull,
  background: `rgb(${vars.color.surfaceContainerHighest})`,
  color: `rgb(${vars.color.onSurfaceVariant})`,
  fontSize: vars.typescale.labelMedium.size,
  fontWeight: '500'
})

export const statusBadgeOn = style({
  background: `rgb(${vars.color.surfaceContainerHighest})`,
  color: `rgb(${vars.color.primary})`
})

export const indexCost = style({
  margin: '0',
  padding: '16px',
  border: `1px solid ${vars.outline.variant}`,
  borderRadius: vars.radius.medium,
  background: 'transparent',
  display: 'flex',
  flexDirection: 'column',
  gap: '12px'
})

export const indexCostList = style({
  display: 'grid',
  gridTemplateColumns: 'repeat(auto-fit, minmax(140px, 1fr))',
  gap: '16px',
  margin: '0'
})

export const indexCostLabel = style({
  color: `rgb(${vars.color.onSurfaceVariant})`,
  fontSize: vars.typescale.bodySmall.size,
  marginBottom: '4px'
})

export const indexCostValue = style({
  margin: '0',
  fontSize: vars.typescale.titleMedium.size,
  fontWeight: '600',
  color: `rgb(${vars.color.onSurface})`
})

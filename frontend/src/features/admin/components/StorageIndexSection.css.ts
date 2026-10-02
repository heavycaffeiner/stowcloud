import { style } from '@vanilla-extract/css'
import { vars } from '@/shared/theme'

export const list = style({
  listStyle: 'none',
  margin: '0',
  padding: '0',
  minWidth: '0',
  overflow: 'hidden',
  border: `1px solid ${vars.color.border.subtle}`,
  borderRadius: vars.radius.md,
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
      background: vars.color.surface.container
    },
    '& + &': {
      borderTop: `1px solid ${vars.color.border.subtle}`
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
  color: vars.color.text.secondary,
  fontWeight: '500',
  fontSize: vars.typography.body.size,
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
  color: vars.color.text.primary
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
  borderRadius: vars.radius.full,
  background: vars.color.surface.fill,
  color: vars.color.text.secondary,
  fontSize: vars.typography.labelSmall.size,
  fontWeight: '500'
})

export const statusBadgeOn = style({
  background: vars.color.surface.fill,
  color: vars.color.accent.solid
})

export const indexCost = style({
  margin: '0',
  padding: '16px',
  border: `1px solid ${vars.color.border.subtle}`,
  borderRadius: vars.radius.md,
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
  color: vars.color.text.secondary,
  fontSize: vars.typography.bodySmall.size,
  marginBottom: '4px'
})

export const indexCostValue = style({
  margin: '0',
  fontSize: vars.typography.title.size,
  fontWeight: '600',
  color: vars.color.text.primary
})

import { style } from '@vanilla-extract/css'
import { vars } from '@/shared/theme'

export const adminSaved = style({
  margin: '0',
  color: vars.color.accent.solid,
  fontSize: vars.typography.bodySmall.size,
  overflowWrap: 'anywhere'
})

export const estimate = style({
  display: 'grid',
  gridTemplateColumns: 'repeat(auto-fit, minmax(140px, 1fr))',
  gap: '16px',
  margin: '0',
  padding: '16px',
  border: `1px solid ${vars.color.border.subtle}`,
  borderRadius: vars.radius.md,
  background: 'transparent'
})

export const estimateLabel = style({
  color: vars.color.text.secondary,
  fontSize: vars.typography.bodySmall.size,
  marginBottom: '4px'
})

export const estimateValue = style({
  margin: '0',
  fontSize: vars.typography.title.size,
  fontWeight: '600',
  color: vars.color.text.primary
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

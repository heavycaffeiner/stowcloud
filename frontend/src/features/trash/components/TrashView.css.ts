import { style } from '@vanilla-extract/css'
import { vars } from '@/shared/theme'

export const checkbox = style({
  display: 'inline-flex',
  alignItems: 'center',
  gap: '8px',
  minHeight: '40px',
  justifyContent: 'center',
  minWidth: '40px'
})

export const rowActions = style({
  display: 'flex',
  flex: 'none',
  alignItems: 'center',
  gap: vars.space.xs
})

export const name = style({
  flex: '1',
  minWidth: '0',
  overflow: 'hidden',
  textOverflow: 'ellipsis',
  whiteSpace: 'nowrap'
})

export const meta = style({
  flexShrink: '0',
  color: vars.color.text.secondary,
  fontSize: vars.typography.bodySmall.size,
  fontWeight: vars.typography.bodySmall.weight,
  lineHeight: vars.typography.bodySmall.lineHeight,
  letterSpacing: vars.typography.bodySmall.tracking,
  whiteSpace: 'nowrap',
  '@media': {
    '(max-width: 480px)': {
      display: 'none'
    }
  }
})

export const operation = style({
  display: 'grid',
  gap: '8px',
  margin: '12px 0',
  padding: '12px 16px',
  border: 'none',
  borderRadius: '12px',
  background: vars.color.surface.raised,
  boxShadow: '0 2px 8px rgba(0, 0, 0, 0.12)'
})

export const operationHeading = style({
  display: 'flex',
  alignItems: 'center',
  justifyContent: 'space-between',
  gap: '8px',
  minWidth: '0'
})

export const operationTitle = style({
  minWidth: '0',
  margin: '0',
  overflowWrap: 'anywhere',
  fontSize: vars.typography.title.size,
  fontWeight: vars.typography.title.weight,
  lineHeight: vars.typography.title.lineHeight,
  letterSpacing: vars.typography.title.tracking
})

export const operationList = style({
  display: 'grid',
  gap: '4px',
  margin: '0',
  padding: '0',
  listStyle: 'none'
})

export const operationItem = style({
  display: 'flex',
  justifyContent: 'space-between',
  gap: '12px',
  overflowWrap: 'anywhere',
  fontSize: vars.typography.bodySmall.size,
  fontWeight: vars.typography.bodySmall.weight,
  lineHeight: vars.typography.bodySmall.lineHeight,
  letterSpacing: vars.typography.bodySmall.tracking
})

export const operationPath = style({
  minWidth: '0',
  overflowWrap: 'anywhere'
})

export const operationResult = style({
  flex: 'none'
})

export const operationError = style({
  color: vars.color.danger.solid
})

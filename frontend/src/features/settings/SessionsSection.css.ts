import { style } from '@vanilla-extract/css'
import { vars } from '../../ui/theme.css'

export const list = style({
  listStyle: 'none',
  padding: '0',
  margin: '0',
  overflow: 'hidden',
  border: `1px solid ${vars.outline.variant}`,
  borderRadius: vars.radius.medium,
  background: 'transparent',
  display: 'flex',
  flexDirection: 'column',
  gap: '0'
})

export const item = style({
  display: 'flex',
  alignItems: 'center',
  justifyContent: 'space-between',
  gap: '16px',
  minBlockSize: '56px',
  padding: '12px 16px',
  borderRadius: '0',
  background: 'transparent',
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
      alignItems: 'stretch',
      flexDirection: 'column',
      padding: '12px'
    }
  }
})

export const itemMain = style({
  minWidth: '0'
})

export const detail = style({
  margin: '4px 0 0',
  color: `rgb(${vars.color.onSurfaceVariant})`,
  fontSize: vars.typescale.bodySmall.size,
  lineHeight: vars.typescale.bodySmall.lineHeight,
  overflowWrap: 'anywhere'
})

export const badge = style({
  display: 'inline-flex',
  alignItems: 'center',
  minBlockSize: '24px',
  marginInlineStart: '8px',
  paddingInline: '8px',
  borderRadius: vars.shape.cornerFull,
  background: `rgb(${vars.color.surfaceContainerHighest})`,
  color: `rgb(${vars.color.onSurfaceVariant})`,
  fontSize: vars.typescale.labelSmall.size,
  fontWeight: vars.typescale.labelSmall.weight,
  lineHeight: vars.typescale.labelSmall.lineHeight,
  whiteSpace: 'nowrap',
  verticalAlign: 'middle'
})

export const error = style({
  margin: '0',
  color: `rgb(${vars.color.error})`,
  overflowWrap: 'anywhere'
})

export const root = style({
  display: 'flex',
  flexDirection: 'column',
  gap: '16px',
  minWidth: '0'
})

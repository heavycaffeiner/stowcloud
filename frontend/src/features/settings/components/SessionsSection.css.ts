import { style } from '@vanilla-extract/css'
import { vars } from '@/shared/theme'

export const list = style({
  listStyle: 'none',
  padding: '0',
  margin: '0',
  overflow: 'hidden',
  border: `1px solid ${vars.color.border.subtle}`,
  borderRadius: vars.radius.md,
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
      background: vars.color.surface.container
    },
    '& + &': {
      borderTop: `1px solid ${vars.color.border.subtle}`
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
  color: vars.color.text.secondary,
  fontSize: vars.typography.bodySmall.size,
  lineHeight: vars.typography.bodySmall.lineHeight,
  overflowWrap: 'anywhere'
})

export const badge = style({
  display: 'inline-flex',
  alignItems: 'center',
  minBlockSize: '24px',
  marginInlineStart: '8px',
  paddingInline: '8px',
  borderRadius: vars.radius.full,
  background: vars.color.surface.fill,
  color: vars.color.text.secondary,
  fontSize: vars.typography.caption.size,
  fontWeight: vars.typography.caption.weight,
  lineHeight: vars.typography.caption.lineHeight,
  whiteSpace: 'nowrap',
  verticalAlign: 'middle'
})

export const error = style({
  margin: '0',
  color: vars.color.danger.solid,
  overflowWrap: 'anywhere'
})

export const root = style({
  display: 'flex',
  flexDirection: 'column',
  gap: '16px',
  minWidth: '0'
})

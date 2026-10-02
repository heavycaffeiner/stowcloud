import { style } from '@vanilla-extract/css'
import { vars } from '@/shared/theme'

export const itemActions = style({
  display: 'flex',
  flexWrap: 'wrap',
  alignItems: 'center',
  justifyContent: 'flex-start',
  gap: '8px',
  '@media': {
    '(max-width: 599.98px)': {
      alignSelf: 'stretch',
      alignItems: 'stretch'
    }
  }
})

export const actions = style({
  display: 'flex',
  flexWrap: 'wrap',
  alignItems: 'center',
  justifyContent: 'flex-start',
  gap: '8px',
  marginTop: '0',
  '@media': {
    '(max-width: 599.98px)': {
      alignItems: 'stretch'
    }
  }
})

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

export const error = style({
  margin: '0',
  color: vars.color.danger.solid,
  overflowWrap: 'anywhere'
})

export const copyFeedback = style({
  margin: '0',
  color: vars.color.danger.solid,
  overflowWrap: 'anywhere'
})

export const tokenInput = style({
  width: '100%',
  boxSizing: 'border-box',
  padding: '8px 12px',
  minHeight: '40px',
  height: '40px',
  lineHeight: '24px',
  border: '0',
  borderRadius: vars.radius.xs,
  background: vars.color.surface.fill,
  color: vars.color.text.primary,
  font: 'inherit',
  overflowWrap: 'anywhere',
  selectors: {
    '&:focus-visible': {
      outline: `${vars.focusRing.width} solid ${vars.color.focus}`,
      outlineOffset: vars.focusRing.offset
    }
  }
})

export const tokenRow = style({
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

export const name = style({
  minWidth: '0',
  overflowWrap: 'anywhere'
})

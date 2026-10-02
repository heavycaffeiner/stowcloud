import { style } from '@vanilla-extract/css'
import { vars } from '@/shared/theme'

export const actions = style({
  display: 'flex',
  flexWrap: 'wrap',
  alignItems: 'center',
  justifyContent: 'flex-start',
  gap: '8px',
  marginTop: '8px',
  '@media': {
    '(max-width: 599.98px)': {
      alignItems: 'stretch'
    }
  }
})

export const error = style({
  margin: '0',
  color: vars.color.danger.solid,
  overflowWrap: 'anywhere'
})

export const success = style({
  margin: '0',
  color: vars.color.accent.solid,
  overflowWrap: 'anywhere'
})

export const root = style({
  display: 'flex',
  flexDirection: 'column',
  gap: '16px',
  width: '100%',
  maxWidth: '28rem'
})

export const strength = style({
  display: 'flex',
  alignItems: 'center',
  gap: '8px',
  marginTop: '-8px'
})

export const strengthBar = style({
  flex: '1'
})

export const strengthLabel = style({
  flex: '0 0 auto',
  minInlineSize: '48px',
  color: vars.color.text.secondary
})

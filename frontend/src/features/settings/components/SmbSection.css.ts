import { style } from '@vanilla-extract/css'
import { vars } from '../../../ui/theme.css'

export const actions = style({
  display: 'flex',
  flexWrap: 'wrap',
  alignItems: 'center',
  justifyContent: 'flex-start',
  gap: '8px',
  '@media': {
    '(max-width: 599.98px)': {
      alignItems: 'stretch'
    }
  }
})

export const state = style({
  margin: '0',
  padding: '0',
  background: 'transparent',
  color: `rgb(${vars.color.onSurfaceVariant})`,
  fontSize: vars.typescale.bodyMedium.size,
  lineHeight: vars.typescale.bodyMedium.lineHeight,
  overflowWrap: 'anywhere'
})

export const root = style({
  display: 'flex',
  flexDirection: 'column',
  gap: '16px',
  minWidth: '0'
})

export const note = style({
  margin: '0',
  color: `rgb(${vars.color.onSurfaceVariant})`,
  overflowWrap: 'anywhere'
})

export const announce = style({
  margin: '0',
  color: `rgb(${vars.color.onSurfaceVariant})`,
  overflowWrap: 'anywhere'
})

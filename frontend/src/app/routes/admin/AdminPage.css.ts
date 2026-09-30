import { style } from '@vanilla-extract/css'
import { vars } from '../../../ui/theme.css'

export const inner = style({
  display: 'grid',
  gap: '0',
  minWidth: '0'
})

export const denied = style({
  margin: '0',
  color: `rgb(${vars.color.onSurfaceVariant})`,
  overflowWrap: 'anywhere'
})

export const pageError = style({
  margin: '0',
  color: `rgb(${vars.color.error})`,
  overflowWrap: 'anywhere'
})

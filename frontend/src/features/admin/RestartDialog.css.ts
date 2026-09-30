import { style } from '@vanilla-extract/css'
import { vars } from '../../ui/theme.css'

export const status = style({
  display: 'inline-flex',
  alignItems: 'center',
  flexWrap: 'wrap',
  gap: '12px',
  maxWidth: '100%',
  overflowWrap: 'anywhere'
})

export const error = style({
  color: `rgb(${vars.color.error})`,
  overflowWrap: 'anywhere'
})

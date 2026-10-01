import { style } from '@vanilla-extract/css'
import { vars } from './theme.css'

export const body = style({
  display: 'flex',
  flexDirection: 'column',
  gap: '16px',
  minWidth: '0'
})

export const error = style({
  margin: '0',
  color: `rgb(${vars.color.error})`,
  overflowWrap: 'anywhere'
})

import { style } from '@vanilla-extract/css'

export const root = style({
  display: 'flex',
  minWidth: 0,
  whiteSpace: 'nowrap'
})

export const start = style({
  minWidth: 0,
  overflow: 'hidden',
  textOverflow: 'ellipsis',
  whiteSpace: 'nowrap'
})

export const end = style({
  flex: 'none',
  whiteSpace: 'nowrap'
})

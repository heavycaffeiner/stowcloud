import { style } from '@vanilla-extract/css'
import { vars } from './theme.css'

export const root = style({
  vars: {
    '--shape-corner': vars.shape.cornerExtraSmall
  }
})

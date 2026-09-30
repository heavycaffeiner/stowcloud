import { style } from '@vanilla-extract/css'
import { vars } from './theme.css'

export const root = style({
  display: 'block',
  inlineSize: '100%'
})

export const bar = style({
  display: 'block',
  inlineSize: '100%'
})

export const weak = style({
  vars: {
    [vars.color.primary]: vars.color.error
  }
})

export const fair = style({
  vars: {
    [vars.color.primary]: vars.color.tertiary
  }
})

export const strong = style({
  vars: {
    [vars.color.primary]: vars.color.primary
  }
})

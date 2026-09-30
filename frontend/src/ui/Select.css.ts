import { style } from '@vanilla-extract/css'
import { vars } from './theme.css'

export const item = style({
  selectors: {
    '&::part(container)': {
      alignItems: 'center'
    },
    '&::part(label)': {
      display: 'flex',
      alignItems: 'center',
      alignSelf: 'stretch',
      lineHeight: vars.typescale.labelLarge.lineHeight
    }
  }
})

export const root = style({
  display: 'flex',
  flexDirection: 'column',
  minInlineSize: '0',
  inlineSize: '100%',
  maxInlineSize: '35rem'
})

export const control = style({
  selectors: {
    '&::part(menu)': {
      borderRadius: vars.radius.medium,
      overflow: 'hidden'
    }
  }
})

import { style } from '@vanilla-extract/css'
import { vars } from './theme.css'

export const root = style({
  selectors: {
    '&::part(panel)': {
      borderRadius: vars.radius.large,
      boxShadow: vars.shadow.level4
    },
    '&::part(action)': {
      display: 'flex',
      alignItems: 'center',
      justifyContent: 'flex-end',
      gap: '8px'
    }
  }
})

export const actions = style({
  display: 'inline-flex',
  alignItems: 'center',
  verticalAlign: 'middle',
  gap: '8px'
})

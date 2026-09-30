import { style } from '@vanilla-extract/css'
import { vars } from './theme.css'

export const root = style({
  backgroundColor: `rgb(${vars.color.surfaceContainerHigh})`,
  borderRadius: vars.shape.cornerFull,
  padding: '4px',
  boxSizing: 'border-box',
  display: 'inline-flex',
  gap: '2px',
  vars: {
    [vars.color.outline]: 'transparent'
  }
})

export const segment = style({
  border: 'none !important',
  borderRadius: `${vars.shape.cornerFull} !important`,
  transition: 'background-color 140ms ease, color 140ms ease, box-shadow 140ms ease',
  vars: {
    [vars.color.outline]: 'transparent'
  },
  selectors: {
    '&[selected]': {
      backgroundColor: `rgb(${vars.color.secondaryContainer}) !important`,
      color: `rgb(${vars.color.onSecondaryContainer}) !important`,
      boxShadow: vars.shadow.level1
    },
    '&:not([selected]):hover': {
      backgroundColor: `rgb(${vars.color.surfaceContainerHighest}) !important`
    },
    '&::part(button)': {
      alignItems: 'center',
      justifyContent: 'center'
    },
    '&::part(label)': {
      display: 'flex',
      alignItems: 'center',
      alignSelf: 'stretch',
      lineHeight: vars.typescale.labelLarge.lineHeight
    }
  }
})

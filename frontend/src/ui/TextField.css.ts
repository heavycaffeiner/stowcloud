import { style } from '@vanilla-extract/css'
import * as dialogStyles from './Dialog.css'
import { vars } from './theme.css'

export const onLowSurface = style({})

export const root = style({
  display: 'flex',
  flexDirection: 'column',
  minInlineSize: '0',
  inlineSize: '100%',
  maxInlineSize: '35rem'
})

export const input = style({
  display: 'block',
  inlineSize: '100%',
  minInlineSize: '0',
  selectors: {
    [`${dialogStyles.root} &`]: {
      vars: {
        [vars.color.surface]: vars.color.surfaceContainerHigh
      }
    },
    [`${onLowSurface} &`]: {
      vars: {
        [vars.color.surface]: vars.color.surfaceContainerLow
      }
    }
  }
})

export const error = style({
  margin: '4px 16px 0',
  color: `rgb(${vars.color.error})`,
  fontSize: vars.typescale.bodySmall.size,
  fontWeight: vars.typescale.bodySmall.weight,
  lineHeight: vars.typescale.bodySmall.lineHeight
})

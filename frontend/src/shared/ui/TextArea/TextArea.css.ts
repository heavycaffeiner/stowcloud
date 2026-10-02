import { style } from '@vanilla-extract/css'
import { vars } from '@/shared/theme'

// A text area holds a document rather than a value, so it fills the container.
export const root = style({
  maxInlineSize: 'none'
})

export const monospace = style({
  fontFamily: vars.font.mono
})

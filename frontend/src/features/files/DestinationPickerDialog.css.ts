import { style } from '@vanilla-extract/css'
import { vars } from '../../ui/theme.css'

export const root = style({
  minWidth: 'min(360px, 72vw)'
})

export const prompt = style({
  margin: '0 0 8px',
  fontSize: vars.typescale.bodyMedium.size,
  lineHeight: vars.typescale.bodyMedium.lineHeight
})

export const tree = style({
  maxHeight: '40vh',
  overflowY: 'auto',
  padding: '4px',
  border: 'none',
  background: `rgb(${vars.color.surfaceContainerHigh})`,
  borderRadius: vars.shape.cornerSmall
})

export const status = style({
  minHeight: '20px',
  margin: '8px 0 0',
  color: vars.content.secondary,
  overflowWrap: 'anywhere'
})

export const statusWarn = style({
  minHeight: '20px',
  margin: '8px 0 0',
  color: `rgb(${vars.color.error})`,
  overflowWrap: 'anywhere'
})

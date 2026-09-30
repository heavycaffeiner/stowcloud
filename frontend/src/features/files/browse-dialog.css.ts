import { style } from '@vanilla-extract/css'
import { vars } from '../../ui/theme.css'

export const root = style({
  vars: {
    [vars.color.surface]: vars.color.surfaceContainerHigh
  }
})

export const body = style({
  display: 'flex',
  flexDirection: 'column',
  gap: '16px',
  paddingBlock: '12px 4px',
  minWidth: 'min(360px, 80vw)'
})

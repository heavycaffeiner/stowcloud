import { style } from '@vanilla-extract/css'
import { vars } from '../../ui/theme.css'

export const externalWarning = style({
  display: 'flex',
  alignItems: 'center',
  gap: '8px',
  color: `rgb(${vars.color.tertiary})`
})

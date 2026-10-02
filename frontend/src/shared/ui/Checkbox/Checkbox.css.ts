import { style } from '@vanilla-extract/css'
import { typography, vars } from '@/shared/theme'

const colors = {
  '--checkbox-color': vars.color.accent.solid,
  '--checkbox-icon-color': vars.color.accent.onSolid
}

export const root = style({ vars: colors })

export const body = style({
  alignItems: 'center',
  minBlockSize: vars.density.row
})

export const label = style({
  ...typography('bodyLarge'),
  color: vars.color.text.primary
})

export const indicator = style({
  vars: colors,
  display: 'inline-flex',
  pointerEvents: 'none'
})

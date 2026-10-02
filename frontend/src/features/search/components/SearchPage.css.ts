import { style } from '@vanilla-extract/css'
import { typography, vars } from '@/shared/theme'

export const root = style({
  display: 'flex',
  minHeight: 0,
  height: '100%',
  minWidth: 0,
  overflow: 'hidden',
  padding: vars.layout.pagePad
})

export const inner = style({
  display: 'flex',
  flexDirection: 'column',
  gap: vars.space.md,
  width: '100%',
  minWidth: 0,
  minHeight: 0
})

export const header = style({
  display: 'flex',
  alignItems: 'center',
  gap: vars.space.sm
})

export const title = style({
  margin: 0,
  ...typography('heading')
})

import { style } from '@vanilla-extract/css'
import { typography, vars } from '@/shared/theme'

export const root = style({
  minWidth: 'min(360px, 72vw)'
})

export const prompt = style({
  margin: `0 0 ${vars.space.sm}`,
  ...typography('body')
})

export const tree = style({
  maxHeight: '40vh',
  overflowY: 'auto',
  padding: vars.space.xs,
  border: 'none',
  background: vars.color.surface.overlay,
  borderRadius: vars.radius.sm
})

export const status = style({
  margin: `${vars.space.sm} 0 0`,
  color: vars.color.text.secondary,
  ...typography('body'),
  overflowWrap: 'anywhere'
})

// Applied together with status.
export const statusWarn = style({
  color: vars.color.danger.solid
})

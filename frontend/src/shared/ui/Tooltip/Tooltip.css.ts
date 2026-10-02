import { style } from '@vanilla-extract/css'
import { typography, vars } from '@/shared/theme'

// Mantine sets its tooltip color inline, so the background is set here rather than through its variable.
export const tooltip = style({
  ...typography('bodySmall'),
  maxInlineSize: '240px',
  padding: `${vars.space.xs} ${vars.space.sm}`,
  borderRadius: vars.radius.xs,
  background: vars.color.surface.inverse,
  color: vars.color.text.inverse,
  overflow: 'hidden',
  textOverflow: 'ellipsis'
})

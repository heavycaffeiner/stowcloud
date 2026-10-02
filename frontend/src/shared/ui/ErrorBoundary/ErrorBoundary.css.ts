import { style } from '@vanilla-extract/css'
import { vars } from '@/shared/theme'

export const notice = style({
  display: 'flex',
  flexWrap: 'wrap',
  alignItems: 'center',
  justifyContent: 'space-between',
  gap: `${vars.space.sm} ${vars.space.lg}`,
  paddingBlock: vars.space.md,
  paddingInline: vars.space.lg,
  border: `${vars.stroke.thin} solid ${vars.color.border.subtle}`,
  borderRadius: vars.radius.sm,
  background: vars.color.surface.raised,
  color: vars.color.text.primary
})

export const message = style({
  margin: 0,
  overflowWrap: 'anywhere'
})

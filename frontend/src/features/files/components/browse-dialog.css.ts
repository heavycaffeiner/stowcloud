import { style } from '@vanilla-extract/css'
import { vars } from '@/shared/theme'

export const body = style({
  display: 'flex',
  flexDirection: 'column',
  gap: vars.space.lg,
  paddingBlock: `${vars.space.md} ${vars.space.xs}`,
  minInlineSize: 'min(22.5rem, 80vw)'
})

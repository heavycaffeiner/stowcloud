import { style, styleVariants } from '@vanilla-extract/css'
import { typography, vars } from '@/shared/theme'

export const root = style({
  vars: { '--badge-radius': vars.radius.sm },
  blockSize: 'auto',
  maxInlineSize: '100%',
  paddingBlock: vars.space.xxs,
  paddingInline: vars.space.sm,
  textTransform: 'none',
  ...typography('labelSmall')
})

export const label = style({
  whiteSpace: 'normal',
  overflowWrap: 'anywhere'
})

export const tone = styleVariants({
  neutral: { vars: { '--badge-bg': vars.color.surface.fill, '--badge-color': vars.color.text.secondary } },
  accent: { vars: { '--badge-bg': vars.color.selection.bg, '--badge-color': vars.color.selection.fg } },
  danger: { vars: { '--badge-bg': vars.color.danger.soft, '--badge-color': vars.color.danger.onSoft } }
})

import { style } from '@vanilla-extract/css'
import { media, typography, vars } from '@/shared/theme'

export const tabs = style({
  display: 'flex',
  gap: vars.space.xs,
  marginBlock: `${vars.space.lg} ${vars.space.xl}`,
  padding: vars.space.xs,
  overflowX: 'auto',
  '@media': { [media.compact]: { marginBlockEnd: vars.space.lg } }
})

export const tab = style({
  display: 'flex',
  flex: 'none',
  flexDirection: 'column',
  alignItems: 'center',
  justifyContent: 'center',
  gap: vars.space.xs,
  minInlineSize: vars.density.row,
  minBlockSize: vars.layout.navBar,
  paddingBlock: vars.space.sm,
  paddingInline: vars.space.md,
  borderRadius: vars.radius.sm,
  color: vars.color.text.secondary,
  ...typography('label'),
  whiteSpace: 'nowrap',
  transition: `background-color ${vars.motion.short} ${vars.motion.easing}`,
  selectors: {
    '&:hover': { background: vars.color.surface.fill },
    '&[aria-current="page"]': { background: vars.color.selection.bg, color: vars.color.selection.fg }
  },
  '@media': { [media.compact]: { paddingInline: vars.space.sm } }
})

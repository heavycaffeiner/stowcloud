import { style } from '@vanilla-extract/css'
import { media, typography, vars } from '@/shared/theme'

export const adminSectionSubhead = style({
  margin: `${vars.space.xxl} 0 ${vars.space.sm}`,
  color: vars.color.text.primary,
  ...typography('titleSmall')
})

export const adminSectionStatus = style({
  margin: `${vars.space.sm} 0 0`,
  color: vars.color.accent.solid,
  overflowWrap: 'anywhere'
})

export const adminSectionStatusError = style({
  color: vars.color.danger.solid
})

export const nav = style({
  position: 'sticky',
  top: 0,
  zIndex: 2,
  margin: `0 0 ${vars.space.lg}`,
  padding: `${vars.space.sm} 0`,
  background: `color-mix(in srgb, ${vars.color.surface.page} 95%, transparent)`,
  backdropFilter: 'blur(12px)'
})

export const navItems = style({
  display: 'flex',
  gap: vars.space.sm,
  overflowX: 'auto',
  padding: vars.space.xxs,
  scrollbarWidth: 'none'
})

export const navButton = style({
  flex: 'none'
})

export const field = style({
  width: '100%'
})

export const other = style({
  display: 'flex',
  flexDirection: 'column',
  gap: vars.space.md,
  margin: `0 0 ${vars.space.lg}`
})

export const otherLabel = style({
  color: vars.color.text.secondary,
  fontFamily: vars.font.mono,
  ...typography('bodySmall')
})

export const otherValue = style({
  margin: 0,
  overflowWrap: 'anywhere'
})

export const reason = style({
  color: vars.color.text.secondary,
  ...typography('bodySmall'),
  overflowWrap: 'anywhere'
})

export const emptyNote = style({
  color: vars.color.text.secondary,
  ...typography('bodySmall'),
  overflowWrap: 'anywhere',
  margin: 0
})

export const findings = style({
  display: 'grid',
  gap: vars.space.xs,
  margin: `${vars.space.sm} 0 0`,
  padding: 0,
  listStyle: 'none'
})

export const finding = style({
  margin: 0,
  padding: `${vars.space.sm} ${vars.space.md}`,
  borderInlineStart: `${vars.space.xs} solid ${vars.color.border.strong}`,
  borderRadius: 0,
  background: 'transparent',
  color: vars.color.text.primary,
  ...typography('bodySmall'),
  overflowWrap: 'anywhere'
})

export const findingBlock = style({
  borderInlineStartColor: vars.color.danger.solid
})

export const findingOk = style({
  borderInlineStartColor: vars.color.accent.solid
})

export const findingKind = style({
  marginInlineEnd: vars.space.xs
})

export const findingField = style({
  marginInlineEnd: vars.space.xs,
  fontFamily: vars.font.mono
})

export const endpoints = style({
  display: 'flex',
  flexDirection: 'column',
  gap: vars.space.xs,
  width: '100%',
  marginTop: vars.space.sm
})

export const endpointRow = style({
  display: 'flex',
  alignItems: 'center',
  gap: vars.space.sm,
  width: '100%',
  minWidth: 0,
  '@media': {
    [media.compact]: {
      alignItems: 'flex-start'
    }
  }
})

export const endpointUri = style({
  flex: 1,
  minWidth: 0,
  fontFamily: vars.font.mono,
  overflowWrap: 'anywhere',
  userSelect: 'all'
})

export const announce = style({
  minBlockSize: '1.25em',
  margin: 0,
  color: vars.color.accent.solid,
  overflowWrap: 'anywhere'
})

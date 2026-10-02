import { style } from '@vanilla-extract/css'
import { media, typography, vars } from '@/shared/theme'

export const issued = style({
  padding: vars.space.lg,
  marginBottom: vars.space.lg,
  borderRadius: vars.radius.md,
  background: vars.color.selection.bg,
  color: vars.color.selection.fg,
  '@media': {
    [media.compact]: {
      padding: vars.space.md
    }
  }
})

export const issuedNote = style({
  margin: `0 0 ${vars.space.sm}`,
  overflowWrap: 'anywhere'
})

export const urlRow = style({
  display: 'flex',
  alignItems: 'center',
  gap: vars.space.sm,
  minWidth: 0
})

export const url = style({
  selectors: { [`${urlRow} &`]: { flex: 1 } }
})

export const copyFeedback = style({
  margin: `${vars.space.sm} 0 0`,
  ...typography('bodySmall')
})

export const copyFeedbackError = style({
  color: vars.color.danger.solid,
  overflowWrap: 'anywhere'
})

export const error = style({
  color: vars.color.danger.solid,
  overflowWrap: 'anywhere'
})

export const loading = style({
  display: 'flex',
  justifyContent: 'center',
  padding: vars.space.xl
})

export const empty = style({
  color: vars.color.text.secondary,
  overflowWrap: 'anywhere'
})

export const list = style({
  display: 'flex',
  flexDirection: 'column',
  gap: vars.space.sm,
  minWidth: 0,
  margin: `0 0 ${vars.space.lg}`,
  padding: 0,
  listStyle: 'none'
})

export const item = style({
  minWidth: 0,
  padding: vars.space.md,
  border: 'none',
  borderRadius: vars.radius.md,
  background: vars.color.surface.raised,
  boxShadow: vars.elevation.sm,
  transition: `background-color ${vars.motion.short} ${vars.motion.easing}`,
  selectors: {
    '&:hover': {
      background: vars.color.surface.overlay
    }
  }
})

export const itemRow = style({
  display: 'flex',
  alignItems: 'flex-start',
  gap: vars.space.md,
  minWidth: 0,
  '@media': {
    [media.compact]: {
      flexWrap: 'wrap'
    }
  }
})

export const itemMain = style({
  display: 'flex',
  flex: '1 1 auto',
  flexDirection: 'column',
  minWidth: 0
})

export const itemLabel = style({
  color: vars.color.text.primary,
  fontWeight: vars.font.weight.medium,
  overflowWrap: 'anywhere'
})

export const itemMeta = style({
  color: vars.color.text.secondary,
  ...typography('bodySmall'),
  overflowWrap: 'anywhere'
})

export const itemActions = style({
  display: 'flex',
  flex: '0 0 auto',
  alignItems: 'center',
  flexWrap: 'wrap',
  justifyContent: 'flex-end',
  '@media': {
    [media.compact]: {
      width: '100%'
    }
  }
})

export const linkForm = style({
  display: 'flex',
  flexDirection: 'column',
  gap: vars.space.md,
  minWidth: 0,
  padding: vars.space.md,
  borderRadius: vars.radius.md,
  background: vars.color.surface.container
})

export const createTitle = style({
  margin: 0,
  ...typography('title')
})

export const permRow = style({
  display: 'flex',
  alignItems: 'center',
  flexWrap: 'wrap',
  gap: `${vars.space.md} ${vars.space.lg}`
})

export const hint = style({
  margin: 0,
  color: vars.color.text.secondary,
  ...typography('bodySmall'),
  overflowWrap: 'anywhere'
})

export const editActions = style({
  display: 'flex',
  alignItems: 'center',
  justifyContent: 'flex-end',
  flexWrap: 'wrap',
  gap: vars.space.sm
})

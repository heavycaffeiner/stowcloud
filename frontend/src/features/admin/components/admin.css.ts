import { style } from '@vanilla-extract/css'
import { fadeInUp, media, typography, vars } from '@/shared/theme'

export const section = style({
  animation: `${fadeInUp} ${vars.motion.medium} ${vars.motion.easing}`,
  display: 'flex',
  flexDirection: 'column',
  gap: vars.space.lg,
  minWidth: 0,
  marginBlock: 0
})

export const sectionHeader = style({
  display: 'flex',
  alignItems: 'center',
  justifyContent: 'space-between',
  gap: vars.space.xl,
  minWidth: 0,
  '@media': {
    [media.compact]: {
      flexDirection: 'column',
      alignItems: 'stretch',
      gap: vars.space.md
    }
  }
})

export const sectionFieldHint = style({
  margin: 0,
  color: vars.color.text.secondary,
  ...typography('bodySmall')
})

export const sectionError = style({
  ...typography('bodySmall'),
  margin: `${vars.space.sm} 0 0`,
  color: vars.color.danger.solid,
  overflowWrap: 'anywhere'
})

export const list = style({
  listStyle: 'none',
  margin: 0,
  padding: 0,
  overflow: 'hidden',
  border: `${vars.stroke.thin} solid ${vars.color.border.subtle}`,
  borderRadius: vars.radius.md,
  background: 'transparent',
  display: 'flex',
  flexDirection: 'column',
  gap: 0
})

export const item = style({
  minWidth: 0,
  selectors: {
    '& + &': {
      borderTop: `${vars.stroke.thin} solid ${vars.color.border.subtle}`
    }
  }
})

export const row = style({
  display: 'flex',
  alignItems: 'center',
  flexWrap: 'wrap',
  gap: vars.space.md,
  minWidth: 0
})

export const rowName = style({
  minWidth: 0,
  overflowWrap: 'anywhere'
})

export const rowActions = style({
  display: 'flex',
  alignItems: 'center',
  justifyContent: 'flex-end',
  flexWrap: 'wrap',
  gap: vars.space.sm,
  flex: '0 0 auto',
  '@media': {
    [media.compact]: {
      width: '100%',
      justifyContent: 'flex-start'
    }
  }
})

export const form = style({
  display: 'flex',
  flexDirection: 'column',
  gap: vars.space.lg,
  width: `min(${vars.layout.form}, 100%)`,
  minWidth: 0
})

export const empty = style({
  display: 'flex',
  flexDirection: 'column',
  alignItems: 'center',
  gap: vars.space.sm,
  padding: `${vars.space.xxl} ${vars.space.lg}`,
  color: vars.color.text.secondary,
  textAlign: 'center',
  border: `${vars.stroke.thin} dashed ${vars.color.border.subtle}`,
  borderRadius: vars.radius.md,
  background: 'transparent'
})

export const emptyText = style({
  margin: 0
})

export const sectionHeaderAction = style({
  '@media': {
    [media.compact]: {
      alignSelf: 'flex-start'
    }
  }
})

export const sectionTitle = style({
  margin: 0,
  color: vars.color.text.primary,
  ...typography('titleLarge')
})

export const hint = style({
  maxWidth: vars.layout.measure,
  margin: 0,
  color: vars.color.text.secondary,
  ...typography('bodySmall'),
  overflowWrap: 'anywhere'
})

export const error = style({
  margin: 0,
  color: vars.color.danger.solid,
  ...typography('bodySmall'),
  overflowWrap: 'anywhere'
})

export const warning = style({
  margin: 0,
  padding: `${vars.space.md} ${vars.space.lg}`,
  borderRadius: vars.radius.xs,
  background: vars.color.danger.soft,
  color: vars.color.danger.onSoft,
  overflowWrap: 'anywhere'
})

export const storageToggleRow = style({
  display: 'flex',
  alignItems: 'center',
  gap: vars.space.md,
  '@media': {
    [media.compact]: {
      alignItems: 'flex-start',
      flexDirection: 'column'
    }
  }
})

// A row of labelled figures, such as sizes and counts, laid out as a description list.
export const figures = style({
  display: 'grid',
  gridTemplateColumns: 'repeat(auto-fit, minmax(140px, 1fr))',
  gap: vars.space.lg,
  margin: 0
})

export const figureLabel = style({
  marginBottom: vars.space.xs,
  color: vars.color.text.secondary,
  ...typography('bodySmall')
})

export const figureValue = style({
  margin: 0,
  ...typography('title'),
  fontWeight: vars.font.weight.bold,
  color: vars.color.text.primary
})

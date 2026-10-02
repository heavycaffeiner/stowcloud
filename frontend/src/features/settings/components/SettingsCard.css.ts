import { style } from '@vanilla-extract/css'
import { media, typography, vars } from '@/shared/theme'

export const card = style({
  display: 'flex',
  flexDirection: 'column',
  gap: vars.space.lg,
  padding: `${vars.space.xl} 0`,
  minWidth: 0,
  border: 0,
  borderBottom: `${vars.stroke.thin} solid ${vars.color.border.subtle}`,
  borderRadius: 0,
  background: 'transparent',
  color: vars.color.text.primary,
  boxShadow: 'none',
  selectors: {
    '&:last-child': {
      borderBottom: 0
    }
  },
  '@media': {
    [media.compact]: {
      padding: `${vars.space.lg} 0`,
      gap: vars.space.md
    }
  }
})

export const cardHead = style({
  display: 'flex',
  alignItems: 'flex-start',
  gap: vars.space.lg,
  minWidth: 0,
  marginBottom: vars.space.xs,
  '@media': {
    [media.compact]: {
      gap: vars.space.md
    }
  }
})

export const cardTitle = style({
  margin: `0 0 ${vars.space.xxs}`,
  color: vars.color.text.primary,
  ...typography('titleLarge'),
  overflowWrap: 'anywhere'
})

export const cardMeta = style({
  flex: '1 1 auto',
  minWidth: 0,
  overflowWrap: 'anywhere'
})

export const text = style({
  overflowWrap: 'anywhere',
  margin: 0
})

// Places a StowBadge in a card head or after inline text.
export const badge = style({
  marginInlineStart: vars.space.sm,
  verticalAlign: 'middle',
  selectors: {
    [`${cardHead} > &`]: {
      flex: 'none',
      marginInlineStart: 'auto'
    }
  },
  '@media': {
    [media.compact]: {
      selectors: {
        [`${cardHead} > &`]: {
          marginInlineStart: 0
        }
      }
    }
  }
})

// The column of blocks inside a card body.
export const section = style({
  display: 'flex',
  flexDirection: 'column',
  gap: vars.space.lg,
  minWidth: 0
})

// A wrapping row of short items, such as a badge or a count beside its action.
export const cluster = style({
  display: 'flex',
  alignItems: 'center',
  flexWrap: 'wrap',
  gap: vars.space.lg
})

export const muted = style([text, { color: vars.color.text.secondary }])

export const error = style([text, { color: vars.color.danger.solid }])

export const warning = style({
  display: 'flex',
  alignItems: 'flex-start',
  gap: vars.space.sm,
  margin: 0,
  padding: vars.space.md,
  borderRadius: vars.radius.xs,
  background: vars.color.danger.soft,
  color: vars.color.danger.onSoft,
  overflowWrap: 'anywhere'
})

// A bordered list of rows, such as sessions or app passwords.
export const list = style({
  listStyle: 'none',
  display: 'flex',
  flexDirection: 'column',
  margin: 0,
  padding: 0,
  overflow: 'hidden',
  border: `${vars.stroke.thin} solid ${vars.color.border.subtle}`,
  borderRadius: vars.radius.md
})

export const item = style({
  display: 'flex',
  alignItems: 'center',
  justifyContent: 'space-between',
  gap: vars.space.lg,
  minBlockSize: vars.density.row,
  padding: `${vars.space.md} ${vars.space.lg}`,
  transition: `background-color ${vars.motion.short} ${vars.motion.easing}`,
  selectors: {
    '&:hover': {
      background: vars.color.surface.container
    },
    '& + &': {
      borderTop: `${vars.stroke.thin} solid ${vars.color.border.subtle}`
    }
  },
  '@media': {
    [media.compact]: {
      alignItems: 'stretch',
      flexDirection: 'column',
      padding: vars.space.md
    }
  }
})

export const itemMain = style({
  minWidth: 0
})

export const itemDetail = style({
  margin: `${vars.space.xs} 0 0`,
  color: vars.color.text.secondary,
  ...typography('bodySmall'),
  overflowWrap: 'anywhere'
})

// A read-only field followed by its copy button.
export const copyRow = style({
  display: 'flex',
  alignItems: 'center',
  gap: vars.space.sm,
  minWidth: 0,
  '@media': {
    [media.compact]: {
      alignItems: 'stretch'
    }
  }
})

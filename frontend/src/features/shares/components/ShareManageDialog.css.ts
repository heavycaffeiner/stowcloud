import { style } from '@vanilla-extract/css'
import { vars } from '@/shared/theme'

export const issued = style({
  padding: '16px',
  marginBottom: '16px',
  borderRadius: vars.radius.md,
  background: vars.color.selection.bg,
  color: vars.color.selection.fg,
  '@media': {
    '(max-width: 599.98px)': {
      padding: '12px'
    }
  }
})

export const issuedNote = style({
  margin: '0 0 8px',
  overflowWrap: 'anywhere'
})

export const urlRow = style({
  display: 'flex',
  alignItems: 'center',
  gap: '8px',
  minWidth: '0'
})

export const url = style({
  display: 'block',
  flex: '1 1 auto',
  minWidth: '0',
  maxWidth: '100%',
  boxSizing: 'border-box',
  padding: '8px',
  border: '0',
  borderRadius: vars.radius.xs,
  background: vars.color.surface.page,
  color: vars.color.text.primary,
  font: 'inherit',
  overflowWrap: 'anywhere',
  resize: 'vertical',
  userSelect: 'all',
  whiteSpace: 'pre-wrap',
  selectors: {
    '&:focus-visible': {
      outline: `${vars.focusRing.width} solid ${vars.color.focus}`,
      outlineOffset: vars.focusRing.offset
    }
  }
})

export const copyFeedback = style({
  margin: '8px 0 0',
  fontSize: vars.typography.bodySmall.size
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
  padding: '24px'
})

export const empty = style({
  color: vars.color.text.secondary,
  overflowWrap: 'anywhere'
})

export const list = style({
  display: 'flex',
  flexDirection: 'column',
  gap: '8px',
  minWidth: '0',
  margin: '0 0 16px',
  padding: '0',
  listStyle: 'none'
})

export const item = style({
  minWidth: '0',
  padding: '12px',
  border: 'none',
  borderRadius: vars.radius.md,
  background: vars.color.surface.raised,
  boxShadow: '0 2px 6px rgba(0, 0, 0, .08)',
  transition: 'background-color 140ms ease',
  selectors: {
    '&:hover': {
      background: vars.color.surface.overlay
    }
  },
  '@media': {
    '(max-width: 599.98px)': {
      padding: '12px'
    }
  }
})

export const itemRow = style({
  display: 'flex',
  alignItems: 'flex-start',
  gap: '12px',
  minWidth: '0',
  '@media': {
    '(max-width: 599.98px)': {
      flexWrap: 'wrap'
    }
  }
})

export const itemMain = style({
  display: 'flex',
  flex: '1 1 auto',
  flexDirection: 'column',
  minWidth: '0',
  '@media': {
    '(max-width: 599.98px)': {
      flexBasis: 'calc(100% - 36px)'
    }
  }
})

export const itemLabel = style({
  color: vars.color.text.primary,
  fontWeight: '500',
  overflowWrap: 'anywhere'
})

export const itemMeta = style({
  color: vars.color.text.secondary,
  fontSize: vars.typography.bodySmall.size,
  overflowWrap: 'anywhere'
})

export const itemActions = style({
  display: 'flex',
  flex: '0 0 auto',
  alignItems: 'center',
  flexWrap: 'wrap',
  justifyContent: 'flex-end',
  '@media': {
    '(max-width: 599.98px)': {
      width: '100%',
      justifyContent: 'flex-end'
    }
  }
})

export const createForm = style({
  display: 'flex',
  flexDirection: 'column',
  gap: '12px',
  minWidth: '0',
  padding: '12px',
  borderRadius: vars.radius.md,
  background: vars.color.surface.container,
  '@media': {
    '(max-width: 599.98px)': {
      padding: '12px'
    }
  }
})

export const editForm = style({
  display: 'flex',
  flexDirection: 'column',
  gap: '12px',
  minWidth: '0',
  padding: '12px',
  borderRadius: vars.radius.md,
  background: vars.color.surface.container,
  '@media': {
    '(max-width: 599.98px)': {
      padding: '12px'
    }
  }
})

export const createTitle = style({
  margin: '0',
  fontSize: vars.typography.title.size,
  fontWeight: vars.typography.title.weight,
  lineHeight: vars.typography.title.lineHeight
})

export const permRow = style({
  display: 'flex',
  alignItems: 'center',
  flexWrap: 'wrap',
  gap: '12px 16px'
})

export const hint = style({
  margin: '0',
  color: vars.color.text.secondary,
  fontSize: vars.typography.bodySmall.size,
  overflowWrap: 'anywhere'
})

export const editActions = style({
  display: 'flex',
  alignItems: 'center',
  justifyContent: 'flex-end',
  flexWrap: 'wrap',
  gap: '8px'
})

import { style } from '@vanilla-extract/css'
import { trailingJustify, trailingWidth } from '@/shared/ui/ListItem/ListItem.css'
import { vars } from '@/shared/theme'

export const empty = style({
  display: 'flex',
  flexDirection: 'column',
  alignItems: 'center',
  gap: '8px',
  padding: '32px 16px',
  textAlign: 'center',
  border: `1px dashed ${vars.color.border.subtle}`,
  borderRadius: vars.radius.md,
  background: 'transparent',
  color: vars.color.text.secondary
})

export const emptyText = style({
  margin: '0'
})

export const list = style({
  listStyle: 'none',
  margin: '0',
  padding: '0',
  minWidth: '0',
  overflow: 'hidden',
  border: `1px solid ${vars.color.border.subtle}`,
  borderRadius: vars.radius.md,
  background: 'transparent',
  display: 'flex',
  flexDirection: 'column',
  gap: '0'
})

export const item = style({
  minWidth: '0',
  background: 'transparent',
  borderRadius: '0',
  display: 'flex',
  alignItems: 'center',
  justifyContent: 'space-between',
  gap: '16px',
  transition: 'background-color 140ms ease',
  padding: '0',
  selectors: {
    '&:hover': {
      background: vars.color.surface.container
    },
    '& + &': {
      borderTop: `1px solid ${vars.color.border.subtle}`
    }
  }
})

export const row = style({
  flex: '1',
  minWidth: '0',
  '@media': {
    '(max-width: 599.98px)': {
      vars: {
        [trailingWidth]: '100%',
        [trailingJustify]: 'flex-start'
      }
    }
  }
})

export const enc = style({
  display: 'flex',
  alignItems: 'center',
  flexWrap: 'wrap',
  gap: '8px',
  minWidth: '0'
})

export const encNote = style({
  display: 'flex',
  alignItems: 'center',
  flexWrap: 'wrap',
  gap: '8px',
  minWidth: '0'
})

export const encSaltRow = style({
  display: 'flex',
  alignItems: 'center',
  flexWrap: 'wrap',
  gap: '8px',
  minWidth: '0',
  marginTop: '4px'
})

export const encSalt = style({
  minWidth: '0',
  maxWidth: '100%',
  overflowWrap: 'anywhere',
  fontFamily: 'ui-monospace, monospace',
  fontSize: vars.typography.bodySmall.size,
  background: vars.color.surface.raised,
  padding: '2px 6px',
  borderRadius: vars.radius.xs
})

export const trash = style({
  display: 'inline-flex',
  alignItems: 'center',
  gap: '8px',
  minHeight: '40px'
})

export const trashLabel = style({
  fontSize: vars.typography.body.size,
  color: vars.color.text.secondary,
  whiteSpace: 'nowrap'
})

export const encSaltLabel = style({
  fontSize: vars.typography.bodySmall.size,
  color: vars.color.text.secondary
})

export const shareBackend = style({
  maxWidth: '100%',
  marginInlineStart: '8px',
  paddingInline: '8px',
  borderRadius: vars.radius.xs,
  background: vars.color.surface.fill,
  color: vars.color.text.secondary,
  fontSize: vars.typography.caption.size,
  overflowWrap: 'anywhere'
})

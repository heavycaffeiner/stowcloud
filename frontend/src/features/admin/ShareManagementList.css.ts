import { style } from '@vanilla-extract/css'
import { trailingJustify, trailingWidth } from '../../ui/ListItem.css'
import { vars } from '../../ui/theme.css'

export const empty = style({
  display: 'flex',
  flexDirection: 'column',
  alignItems: 'center',
  gap: '8px',
  padding: '32px 16px',
  textAlign: 'center',
  border: `1px dashed ${vars.outline.variant}`,
  borderRadius: vars.radius.medium,
  background: 'transparent',
  color: `rgb(${vars.color.onSurfaceVariant})`
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
  border: `1px solid ${vars.outline.variant}`,
  borderRadius: vars.radius.medium,
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
      background: `rgb(${vars.color.surfaceContainerLow})`
    },
    '& + &': {
      borderTop: `1px solid ${vars.outline.variant}`
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
  fontSize: vars.typescale.bodySmall.size,
  background: `rgb(${vars.color.surfaceContainer})`,
  padding: '2px 6px',
  borderRadius: vars.shape.cornerExtraSmall
})

export const trash = style({
  display: 'inline-flex',
  alignItems: 'center',
  gap: '8px',
  minHeight: '40px'
})

export const trashLabel = style({
  fontSize: vars.typescale.bodyMedium.size,
  color: `rgb(${vars.color.onSurfaceVariant})`,
  whiteSpace: 'nowrap'
})

export const encSaltLabel = style({
  fontSize: vars.typescale.bodySmall.size,
  color: `rgb(${vars.color.onSurfaceVariant})`
})

export const shareBackend = style({
  maxWidth: '100%',
  marginInlineStart: '8px',
  paddingInline: '8px',
  borderRadius: vars.shape.cornerExtraSmall,
  background: `rgb(${vars.color.surfaceContainerHighest})`,
  color: `rgb(${vars.color.onSurfaceVariant})`,
  fontSize: vars.typescale.labelSmall.size,
  overflowWrap: 'anywhere'
})

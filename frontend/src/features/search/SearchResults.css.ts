import { style } from '@vanilla-extract/css'
import { vars } from '../../ui/theme.css'

export const results = style({
  position: 'relative',
  maxHeight: '420px',
  overflowY: 'auto',
  overflowX: 'hidden',
  borderRadius: '10px',
  background: vars.surface.page,
  border: `1px solid ${vars.outline.variant}`,
  outline: 'none',
  '@media': {
    '(max-width: 599.98px)': {
      maxHeight: 'none',
      minHeight: '240px'
    }
  }
})

export const note = style({
  padding: '32px 16px',
  textAlign: 'center',
  color: vars.content.secondary,
  fontSize: vars.typescale.bodyMedium.size,
  lineHeight: vars.typescale.bodyMedium.lineHeight,
  margin: '0'
})

export const spacer = style({
  position: 'relative'
})

export const rows = style({
  listStyle: 'none',
  margin: '0',
  padding: '0',
  width: '100%'
})

export const row = style({
  width: '100%',
  height: '56px',
  display: 'flex',
  alignItems: 'center',
  gap: '12px',
  padding: '0 16px',
  border: 'none',
  background: 'transparent',
  color: vars.content.primary,
  cursor: 'pointer',
  textAlign: 'left',
  boxSizing: 'border-box',
  transition: 'background-color 120ms ease',
  selectors: {
    '&:hover': {
      background: `color-mix(in srgb, ${vars.content.primary} 5%, transparent)`
    },
    '&:focus-visible': {
      background: `color-mix(in srgb, ${vars.content.primary} 8%, transparent)`,
      outline: `${vars.focusRing.width} solid ${vars.state.focus}`,
      outlineOffset: vars.focusRing.insetOffset
    }
  },
  '@media': {
    '(max-width: 599.98px)': {
      height: 'auto',
      minHeight: '64px',
      padding: '10px 12px',
      gap: '10px'
    }
  }
})

export const rowIcon = style({
  display: 'inline-flex',
  alignItems: 'center',
  justifyContent: 'center',
  width: '32px',
  height: '32px',
  borderRadius: '8px',
  background: `color-mix(in srgb, ${vars.content.primary} 6%, transparent)`,
  flex: 'none'
})

export const text = style({
  flex: '1',
  minWidth: '0',
  display: 'flex',
  flexDirection: 'column',
  gap: '2px'
})

export const name = style({
  fontSize: vars.typescale.bodyLarge.size,
  lineHeight: vars.typescale.bodyLarge.lineHeight,
  fontWeight: '500',
  color: vars.content.primary,
  overflow: 'hidden',
  textOverflow: 'ellipsis',
  whiteSpace: 'nowrap'
})

export const folder = style({
  fontSize: vars.typescale.bodySmall.size,
  lineHeight: vars.typescale.bodySmall.lineHeight,
  color: vars.content.secondary,
  overflow: 'hidden',
  textOverflow: 'ellipsis',
  whiteSpace: 'nowrap'
})

export const cell = style({
  display: 'flex',
  flexDirection: 'column',
  alignItems: 'flex-end',
  gap: '2px',
  flex: 'none',
  '@media': {
    '(max-width: 599.98px)': {
      display: 'none'
    }
  }
})

export const size = style({
  fontSize: vars.typescale.bodySmall.size,
  lineHeight: vars.typescale.bodySmall.lineHeight,
  fontWeight: '500',
  color: vars.content.primary
})

export const date = style({
  fontSize: vars.typescale.bodySmall.size,
  lineHeight: vars.typescale.bodySmall.lineHeight,
  color: vars.content.secondary
})

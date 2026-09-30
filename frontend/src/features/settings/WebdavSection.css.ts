import { style } from '@vanilla-extract/css'
import { vars } from '../../ui/theme.css'

export const tokenRow = style({
  display: 'flex',
  alignItems: 'center',
  gap: '8px',
  minWidth: '0',
  '@media': {
    '(max-width: 599.98px)': {
      alignItems: 'stretch'
    }
  }
})

export const root = style({
  display: 'flex',
  flexDirection: 'column',
  gap: '16px',
  minWidth: '0'
})

export const os = style({
  selectors: {
    [`${root} section&`]: {
      display: 'flex',
      flexDirection: 'column',
      gap: '12px',
      padding: '16px 0 0',
      border: '0',
      borderTop: `1px solid ${vars.outline.variant}`,
      borderRadius: '0',
      background: 'transparent',
      boxShadow: 'none'
    }
  }
})

export const heading = style({
  margin: '0',
  fontSize: vars.typescale.titleMedium.size,
  lineHeight: vars.typescale.titleMedium.lineHeight,
  color: `rgb(${vars.color.primary})`,
  fontWeight: '600'
})

export const subheading = style({
  margin: '12px 0 4px',
  fontSize: vars.typescale.titleSmall.size,
  lineHeight: vars.typescale.titleSmall.lineHeight
})

export const steps = style({
  margin: '0',
  paddingInlineStart: '20px',
  display: 'flex',
  flexDirection: 'column',
  gap: '6px',
  color: `rgb(${vars.color.onSurfaceVariant})`,
  fontSize: vars.typescale.bodyMedium.size
})

export const tokenBlock = style({
  maxWidth: '100%',
  margin: '0',
  whiteSpace: 'pre-wrap',
  overflowWrap: 'anywhere'
})

export const credentials = style({
  margin: '0',
  color: `rgb(${vars.color.onSurfaceVariant})`,
  overflowWrap: 'anywhere'
})

export const nfcNote = style({
  margin: '0',
  color: `rgb(${vars.color.onSurfaceVariant})`,
  overflowWrap: 'anywhere'
})

export const announce = style({
  margin: '0',
  color: `rgb(${vars.color.onSurfaceVariant})`,
  overflowWrap: 'anywhere'
})

export const label = style({
  display: 'block',
  marginBottom: '4px',
  color: `rgb(${vars.color.onSurfaceVariant})`
})

export const token = style({
  flex: '1',
  minWidth: '0',
  display: 'inline-flex',
  alignItems: 'center',
  minHeight: '40px',
  margin: '0',
  padding: '8px 12px',
  boxSizing: 'border-box',
  fontFamily: 'ui-monospace, monospace',
  fontSize: vars.typescale.bodyMedium.size,
  borderRadius: vars.shape.cornerExtraSmall,
  background: `rgb(${vars.color.surfaceContainerHighest})`,
  color: `rgb(${vars.color.onSurface})`,
  overflowWrap: 'anywhere'
})

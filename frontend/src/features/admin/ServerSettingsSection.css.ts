import { style } from '@vanilla-extract/css'
import { vars } from '../../ui/theme.css'

export const adminSectionSubhead = style({
  margin: '32px 0 8px',
  color: `rgb(${vars.color.onSurface})`,
  fontSize: vars.typescale.titleSmall.size,
  lineHeight: vars.typescale.titleSmall.lineHeight
})

export const adminSectionStatus = style({
  margin: '8px 0 0',
  color: `rgb(${vars.color.primary})`,
  overflowWrap: 'anywhere'
})

export const adminSectionStatusError = style({
  color: `rgb(${vars.color.error})`
})

export const nav = style({
  position: 'sticky',
  top: '0',
  zIndex: '2',
  margin: '0 0 20px',
  padding: '8px 0',
  background: `color-mix(in srgb, ${vars.surface.page} 95%, transparent)`,
  backdropFilter: 'blur(12px)'
})

export const navItems = style({
  display: 'flex',
  gap: '8px',
  overflowX: 'auto',
  padding: '2px',
  scrollbarWidth: 'none'
})

export const navButton = style({
  display: 'inline-flex',
  alignItems: 'center',
  justifyContent: 'center',
  flex: 'none',
  minBlockSize: vars.control.min,
  padding: '0 16px',
  border: 'none',
  borderRadius: vars.shape.cornerFull,
  color: `rgb(${vars.color.onSurface})`,
  background: `rgb(${vars.color.surfaceContainerHigh})`,
  cursor: 'pointer',
  whiteSpace: 'nowrap',
  fontSize: vars.typescale.labelLarge.size,
  lineHeight: vars.typescale.labelLarge.lineHeight,
  fontWeight: '500',
  transition: 'background-color 120ms ease, color 120ms ease',
  selectors: {
    '&:hover': {
      background: `rgb(${vars.color.surfaceContainerHighest})`,
      color: `rgb(${vars.color.primary})`
    },
    '&:focus-visible': {
      outline: `${vars.focusRing.width} solid ${vars.state.focus}`,
      outlineOffset: vars.focusRing.offset
    }
  },
  '@media': {
    '(max-width: 599.98px)': {
      minBlockSize: '44px'
    }
  }
})

export const pathRow = style({
  display: 'flex',
  gap: '8px',
  alignItems: 'center',
  width: '100%',
  minWidth: '0',
  '@media': {
    '(max-width: 599.98px)': {
      alignItems: 'stretch',
      flexDirection: 'column'
    }
  }
})

export const field = style({
  width: '100%',
  selectors: {
    [`${pathRow} > &`]: {
      width: 'auto',
      flex: '1 1 auto',
      minWidth: '0'
    }
  }
})

export const other = style({
  display: 'flex',
  flexDirection: 'column',
  gap: '12px',
  margin: '0 0 16px'
})

export const otherLabel = style({
  color: `rgb(${vars.color.onSurfaceVariant})`,
  fontFamily: 'ui-monospace, monospace',
  fontSize: vars.typescale.bodySmall.size
})

export const otherValue = style({
  margin: '0',
  overflowWrap: 'anywhere'
})

export const reason = style({
  color: `rgb(${vars.color.onSurfaceVariant})`,
  fontSize: vars.typescale.bodySmall.size,
  overflowWrap: 'anywhere'
})

export const emptyNote = style({
  color: `rgb(${vars.color.onSurfaceVariant})`,
  fontSize: vars.typescale.bodySmall.size,
  overflowWrap: 'anywhere',
  margin: '0'
})

export const findings = style({
  display: 'grid',
  gap: '4px',
  margin: '8px 0 0',
  padding: '0',
  listStyle: 'none'
})

export const finding = style({
  margin: '0',
  padding: '8px 12px',
  borderInlineStart: `4px solid rgb(${vars.color.outline})`,
  borderRadius: '0',
  background: 'transparent',
  color: `rgb(${vars.color.onSurface})`,
  fontSize: vars.typescale.bodySmall.size,
  overflowWrap: 'anywhere'
})

export const findingBlock = style({
  borderInlineStartColor: `rgb(${vars.color.error})`
})

export const findingOk = style({
  borderInlineStartColor: `rgb(${vars.color.primary})`
})

export const findingKind = style({
  marginInlineEnd: '4px'
})

export const findingField = style({
  marginInlineEnd: '4px',
  fontFamily: 'ui-monospace, monospace'
})

export const endpoints = style({
  display: 'flex',
  flexDirection: 'column',
  gap: '4px',
  width: '100%',
  marginTop: '8px'
})

export const endpointRow = style({
  display: 'flex',
  alignItems: 'center',
  gap: '8px',
  width: '100%',
  minWidth: '0',
  '@media': {
    '(max-width: 599.98px)': {
      alignItems: 'flex-start'
    }
  }
})

export const endpointUri = style({
  flex: '1',
  minWidth: '0',
  fontFamily: 'ui-monospace, monospace',
  overflowWrap: 'anywhere',
  userSelect: 'all'
})

export const announce = style({
  minBlockSize: '1.25em',
  margin: '0',
  color: `rgb(${vars.color.primary})`,
  overflowWrap: 'anywhere'
})

export const pathButton = style({
  '@media': {
    '(max-width: 599.98px)': {
      alignSelf: 'flex-start'
    }
  }
})

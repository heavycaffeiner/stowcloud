import { style } from '@vanilla-extract/css'
import { vars } from '@/shared/theme'

export const adminSectionSubhead = style({
  margin: '32px 0 8px',
  color: vars.color.text.primary,
  fontSize: vars.typography.titleSmall.size,
  lineHeight: vars.typography.titleSmall.lineHeight
})

export const adminSectionStatus = style({
  margin: '8px 0 0',
  color: vars.color.accent.solid,
  overflowWrap: 'anywhere'
})

export const adminSectionStatusError = style({
  color: vars.color.danger.solid
})

export const nav = style({
  position: 'sticky',
  top: '0',
  zIndex: '2',
  margin: '0 0 20px',
  padding: '8px 0',
  background: `color-mix(in srgb, ${vars.color.surface.page} 95%, transparent)`,
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
  flex: 'none'
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
  color: vars.color.text.secondary,
  fontFamily: 'ui-monospace, monospace',
  fontSize: vars.typography.bodySmall.size
})

export const otherValue = style({
  margin: '0',
  overflowWrap: 'anywhere'
})

export const reason = style({
  color: vars.color.text.secondary,
  fontSize: vars.typography.bodySmall.size,
  overflowWrap: 'anywhere'
})

export const emptyNote = style({
  color: vars.color.text.secondary,
  fontSize: vars.typography.bodySmall.size,
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
  borderInlineStart: `4px solid ${vars.color.border.strong}`,
  borderRadius: '0',
  background: 'transparent',
  color: vars.color.text.primary,
  fontSize: vars.typography.bodySmall.size,
  overflowWrap: 'anywhere'
})

export const findingBlock = style({
  borderInlineStartColor: vars.color.danger.solid
})

export const findingOk = style({
  borderInlineStartColor: vars.color.accent.solid
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
  color: vars.color.accent.solid,
  overflowWrap: 'anywhere'
})

export const pathButton = style({
  '@media': {
    '(max-width: 599.98px)': {
      alignSelf: 'flex-start'
    }
  }
})

export const selectLabel = style({
  display: 'flex',
  flexDirection: 'column',
  gap: '6px',
  fontSize: vars.typography.body.size,
  color: vars.color.text.primary,
  width: '100%'
})

export const select = style({
  minBlockSize: '48px',
  width: '100%',
  maxWidth: '24rem',
  padding: '10px 16px',
  border: 'none',
  borderRadius: vars.radius.xs,
  background: vars.color.surface.fill,
  color: vars.color.text.primary,
  fontSize: vars.typography.bodyLarge.size,
  cursor: 'pointer',
  selectors: {
    '&:focus-visible': {
      outline: `${vars.focusRing.width} solid ${vars.color.focus}`,
      outlineOffset: vars.focusRing.offset
    }
  }
})

import { style } from '@vanilla-extract/css'
import { fadeInUp, vars } from '../../../ui/theme.css'

export const section = style({
  animation: `${fadeInUp} 220ms cubic-bezier(0.2, 0, 0, 1)`,
  display: 'flex',
  flexDirection: 'column',
  gap: '16px',
  minWidth: '0',
  marginBlock: '0'
})

export const sectionHeader = style({
  display: 'flex',
  alignItems: 'center',
  justifyContent: 'space-between',
  gap: '24px',
  minWidth: '0',
  '@media': {
    '(max-width: 599.98px)': {
      flexDirection: 'column',
      alignItems: 'stretch',
      gap: '12px'
    }
  }
})

export const sectionHint = style({
  margin: '0',
  fontWeight: vars.typescale.bodySmall.weight,
  letterSpacing: vars.typescale.bodySmall.tracking,
  maxWidth: '40rem',
  color: `rgb(${vars.color.onSurfaceVariant})`,
  fontSize: vars.typescale.bodySmall.size,
  lineHeight: vars.typescale.bodySmall.lineHeight,
  overflowWrap: 'anywhere'
})

export const sectionFieldHint = style({
  margin: '0',
  color: `rgb(${vars.color.onSurfaceVariant})`,
  fontSize: vars.typescale.bodySmall.size,
  fontWeight: vars.typescale.bodySmall.weight,
  letterSpacing: vars.typescale.bodySmall.tracking,
  lineHeight: vars.typescale.bodySmall.lineHeight
})

export const sectionError = style({
  fontSize: vars.typescale.bodySmall.size,
  lineHeight: vars.typescale.bodySmall.lineHeight,
  margin: '8px 0 0',
  color: `rgb(${vars.color.error})`,
  overflowWrap: 'anywhere'
})

export const list = style({
  listStyle: 'none',
  margin: '0',
  padding: '0',
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
  selectors: {
    '& + &': {
      borderTop: `1px solid ${vars.outline.variant}`
    }
  }
})

export const row = style({
  display: 'flex',
  alignItems: 'center',
  flexWrap: 'wrap',
  gap: '12px',
  minWidth: '0'
})

export const rowName = style({
  minWidth: '0',
  overflowWrap: 'anywhere'
})

export const rowActions = style({
  display: 'flex',
  alignItems: 'center',
  justifyContent: 'flex-end',
  flexWrap: 'wrap',
  gap: '8px',
  flex: '0 0 auto',
  '@media': {
    '(max-width: 599.98px)': {
      width: '100%',
      justifyContent: 'flex-start'
    }
  }
})

export const chipAction = style({
  margin: '0',
  display: 'inline-flex',
  alignItems: 'center'
})

export const chip = style({
  selectors: {
    'button&': {
      minHeight: vars.control.min,
      border: `1px solid ${vars.outline.variant}`,
      font: 'inherit',
      cursor: 'pointer',
      paddingInline: '12px',
      transition: 'background-color 140ms ease, border-color 140ms ease, opacity 140ms ease, transform 120ms ease'
    },
    'button&:hover': {
      borderColor: vars.outline.default,
      filter: 'brightness(0.92)'
    },
    'button&:active': {
      transform: 'scale(0.95)'
    },
    'button&:focus-visible': {
      outline: `${vars.focusRing.width} solid ${vars.state.focus}`,
      outlineOffset: vars.focusRing.offset
    },
    [`&:has(${chipAction})`]: {
      minHeight: '44px',
      gap: '4px',
      padding: '0 0 0 12px',
      vars: {
        [vars.control.min]: '44px'
      }
    }
  },
  display: 'inline-flex',
  alignItems: 'center',
  justifyContent: 'center',
  minBlockSize: '24px',
  padding: '2px 9px',
  borderRadius: vars.shape.cornerFull,
  background: `rgb(${vars.color.surfaceContainerHighest})`,
  color: `rgb(${vars.color.onSurfaceVariant})`,
  fontSize: vars.typescale.labelSmall.size,
  fontWeight: vars.typescale.labelSmall.weight,
  lineHeight: vars.typescale.labelSmall.lineHeight,
  whiteSpace: 'nowrap',
  '@media': {
    '(max-width: 599.98px)': {
      selectors: {
        'button&': {
          minBlockSize: '44px'
        }
      }
    }
  }
})

export const form = style({
  display: 'flex',
  flexDirection: 'column',
  gap: '16px',
  width: 'min(36rem, 100%)',
  minWidth: '0'
})

export const empty = style({
  display: 'flex',
  flexDirection: 'column',
  alignItems: 'center',
  gap: '8px',
  padding: '32px 16px',
  color: `rgb(${vars.color.onSurfaceVariant})`,
  textAlign: 'center',
  border: `1px dashed ${vars.outline.variant}`,
  borderRadius: vars.radius.medium,
  background: 'transparent'
})

export const emptyText = style({
  margin: '0'
})

export const sectionHeaderAction = style({
  '@media': {
    '(max-width: 599.98px)': {
      alignSelf: 'flex-start'
    }
  }
})

export const sectionTitle = style({
  margin: '0',
  color: `rgb(${vars.color.onSurface})`,
  fontSize: vars.typescale.titleLarge.size,
  fontWeight: vars.typescale.titleLarge.weight,
  letterSpacing: vars.typescale.titleLarge.tracking,
  lineHeight: vars.typescale.titleLarge.lineHeight
})

export const hint = style({
  maxWidth: '40rem',
  margin: '0',
  color: `rgb(${vars.color.onSurfaceVariant})`,
  fontSize: vars.typescale.bodySmall.size,
  lineHeight: vars.typescale.bodySmall.lineHeight,
  overflowWrap: 'anywhere'
})

export const note = style({
  maxWidth: '40rem',
  margin: '0',
  color: `rgb(${vars.color.onSurfaceVariant})`,
  fontSize: vars.typescale.bodySmall.size,
  lineHeight: vars.typescale.bodySmall.lineHeight,
  overflowWrap: 'anywhere'
})

export const error = style({
  margin: '0',
  color: `rgb(${vars.color.error})`,
  fontSize: vars.typescale.bodySmall.size,
  overflowWrap: 'anywhere'
})

export const warning = style({
  margin: '0',
  padding: '12px 16px',
  borderRadius: vars.shape.cornerExtraSmall,
  background: `rgb(${vars.color.errorContainer})`,
  color: `rgb(${vars.color.onErrorContainer})`,
  overflowWrap: 'anywhere'
})

export const storageToggleRow = style({
  display: 'flex',
  alignItems: 'center',
  gap: '12px',
  '@media': {
    '(max-width: 599.98px)': {
      alignItems: 'flex-start',
      flexDirection: 'column'
    }
  }
})

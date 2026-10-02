import { style } from '@vanilla-extract/css'
import { buttonMinHeight } from '../../../ui/Button.css'
import { vars } from '@/shared/theme'

export const root = style({
  width: 'min(100%, 40rem)',
  minWidth: '0',
  overflowX: 'hidden',
  minHeight: '100dvh',
  marginInline: 'auto',
  padding: vars.layout.pagePad,
  '@media': {
    '(max-width: 599.98px)': {
      paddingBlock: '16px',
      vars: {
        [buttonMinHeight]: '44px'
      }
    }
  }
})

export const header = style({
  display: 'flex',
  flexWrap: 'wrap',
  gap: '4px 8px',
  marginBottom: '32px',
  color: vars.color.text.secondary,
  fontSize: vars.typography.body.size,
  fontWeight: vars.typography.body.weight,
  lineHeight: vars.typography.body.lineHeight,
  letterSpacing: vars.typography.body.tracking,
  '@media': {
    '(max-width: 599.98px)': {
      marginBottom: '24px'
    }
  }
})

export const title = style({
  fontSize: vars.typography.heading.size,
  fontWeight: vars.typography.heading.weight,
  lineHeight: vars.typography.heading.lineHeight,
  letterSpacing: vars.typography.heading.tracking,
  margin: '0 0 16px',
  overflowWrap: 'anywhere'
})

export const unlockTitle = style({
  fontSize: vars.typography.heading.size,
  fontWeight: vars.typography.heading.weight,
  lineHeight: vars.typography.heading.lineHeight,
  letterSpacing: vars.typography.heading.tracking,
  margin: '0',
  overflowWrap: 'anywhere'
})

export const status = style({
  margin: '0 0 16px',
  overflowWrap: 'anywhere',
  color: vars.color.text.secondary
})

export const statusError = style({
  color: vars.color.danger.solid
})

export const state = style({
  display: 'flex',
  flexDirection: 'column',
  gap: '16px',
  minWidth: '0'
})

export const unlock = style({
  display: 'flex',
  flexDirection: 'column',
  gap: '16px',
  minWidth: '0',
  maxWidth: '20rem',
  width: '100%'
})

export const stateError = style({
  padding: '16px',
  borderRadius: vars.radius.md,
  background: vars.color.danger.soft,
  color: vars.color.danger.onSoft
})

export const stateText = style({
  margin: '0',
  overflowWrap: 'anywhere'
})

export const unlockActions = style({
  alignSelf: 'flex-start'
})

export const drop = style({
  display: 'flex',
  flexDirection: 'column',
  alignItems: 'flex-start',
  gap: '8px',
  marginBottom: '24px'
})

export const file = style({
  display: 'none'
})

export const list = style({
  display: 'flex',
  flexDirection: 'column',
  gap: '0',
  minWidth: '0',
  margin: '0 0 24px',
  padding: '0',
  overflow: 'hidden',
  border: `1px solid ${vars.color.border.subtle}`,
  borderRadius: vars.radius.md,
  background: 'transparent',
  listStyle: 'none'
})

export const row = style({
  display: 'flex',
  alignItems: 'center',
  gap: '12px',
  minWidth: '0',
  minHeight: '52px',
  padding: '8px 16px',
  borderRadius: '0',
  background: 'transparent',
  borderBottom: `1px solid ${vars.color.border.subtle}`,
  transition: 'background-color 140ms ease',
  selectors: {
    '&:hover': {
      background: vars.color.surface.container
    },
    '&:last-child': {
      borderBottom: 'none'
    }
  },
  '@media': {
    '(max-width: 599.98px)': {
      gridTemplateColumns: 'minmax(0, 1fr) auto',
      paddingInline: '8px'
    }
  }
})

export const rowEmpty = style({
  justifyContent: 'center',
  color: vars.color.text.secondary,
  textAlign: 'center'
})

export const icon = style({
  display: 'inline-flex',
  alignItems: 'center',
  justifyContent: 'center',
  flex: 'none',
  color: vars.color.text.icon
})

export const name = style({
  flex: '1',
  minWidth: '0',
  overflow: 'hidden',
  textOverflow: 'ellipsis',
  whiteSpace: 'nowrap',
  fontSize: vars.typography.body.size,
  color: vars.color.text.primary,
  textAlign: 'start',
  selectors: {
    'button&': {
      border: 'none',
      background: 'transparent',
      cursor: 'pointer',
      padding: '0'
    }
  }
})

export const size = style({
  flex: 'none',
  color: vars.color.text.secondary,
  fontSize: vars.typography.bodySmall.size,
  whiteSpace: 'nowrap',
  fontVariantNumeric: 'tabular-nums',
  '@media': {
    '(max-width: 599.98px)': {
      maxWidth: 'min(42vw, 12rem)'
    }
  }
})

export const action = style({
  flex: 'none'
})

export const crumbs = style({
  marginBottom: '16px',
  minWidth: '0'
})

export const crumbList = style({
  display: 'flex',
  flexWrap: 'wrap',
  alignItems: 'center',
  gap: '4px',
  minWidth: '0',
  maxWidth: '100%',
  margin: '0',
  padding: '0',
  listStyle: 'none'
})

export const crumbItem = style({
  display: 'flex',
  alignItems: 'center',
  gap: '4px',
  minWidth: '0',
  maxWidth: '100%'
})

export const crumb = style({
  minWidth: '0',
  minHeight: '40px',
  maxWidth: '100%',
  padding: '4px 8px',
  overflow: 'hidden',
  border: 'none',
  borderRadius: vars.radius.sm,
  background: 'none',
  color: vars.color.accent.solid,
  font: 'inherit',
  textAlign: 'start',
  textOverflow: 'ellipsis',
  whiteSpace: 'nowrap',
  cursor: 'pointer',
  selectors: {
    '&:hover': {
      background: vars.color.surface.raised
    }
  },
  '@media': {
    '(max-width: 599.98px)': {
      minHeight: '44px'
    }
  }
})

export const folder = style({
  minWidth: '0',
  minHeight: '40px',
  maxWidth: '100%',
  padding: '4px 8px',
  overflow: 'hidden',
  border: 'none',
  borderRadius: vars.radius.sm,
  background: 'none',
  color: vars.color.accent.solid,
  font: 'inherit',
  textAlign: 'start',
  textOverflow: 'ellipsis',
  whiteSpace: 'nowrap',
  cursor: 'pointer',
  selectors: {
    '&:hover': {
      background: vars.color.surface.raised
    }
  },
  '@media': {
    '(max-width: 599.98px)': {
      minHeight: '44px'
    }
  }
})

export const crumbSep = style({
  color: vars.color.text.secondary
})

export const crumbCurrent = style({
  minWidth: '0',
  maxWidth: '100%',
  overflowWrap: 'anywhere',
  fontWeight: vars.typography.label.weight
})

export const stateAction = style({
  alignSelf: 'flex-start'
})

export const rowActions = style({
  '@media': {
    '(max-width: 599.98px)': {
      gridColumn: '1 / -1',
      justifySelf: 'end'
    }
  }
})

export const rowAction = style({
  '@media': {
    '(max-width: 599.98px)': {
      maxWidth: '100%'
    }
  }
})

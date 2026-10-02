import { style } from '@vanilla-extract/css'
import { scaleUp, vars } from '@/shared/theme'

export const page = style({
  minHeight: '100dvh',
  minWidth: '0',
  overflowY: 'auto',
  display: 'flex',
  alignItems: 'center',
  justifyContent: 'center',
  padding: vars.layout.pagePad,
  background: `radial-gradient(circle at 20% 15%, color-mix(in srgb, ${vars.color.accent.solid} 17%, transparent), transparent 24rem), radial-gradient(circle at 85% 80%, color-mix(in srgb, ${vars.color.neutral.solid} 12%, transparent), transparent 28rem), ${vars.color.surface.page}`,
  '@media': {
    '(max-width: 599.98px)': {
      alignItems: 'flex-start',
      paddingBlock: '16px'
    }
  }
})

export const card = style({
  width: 'min(100%, 42rem)',
  minWidth: '0',
  display: 'flex',
  flexDirection: 'column',
  gap: '16px',
  padding: '24px',
  borderRadius: vars.radius.xl,
  background: vars.color.surface.raised,
  border: `1px solid ${vars.color.border.subtle}`,
  boxShadow: vars.elevation.sm,
  animation: `${scaleUp} 240ms cubic-bezier(0.2, 0, 0, 1)`,
  '@media': {
    '(max-width: 599.98px)': {
      gap: '14px',
      padding: '20px 16px',
      borderRadius: vars.radius.xl
    }
  }
})

export const login = style({
  width: 'min(100%, 36rem)',
  marginInline: 'auto',
  borderRadius: vars.radius.lg,
  boxShadow: 'none'
})

export const title = style({
  minWidth: '0',
  margin: '0',
  overflowWrap: 'anywhere',
  fontSize: vars.typography.heading.size,
  fontWeight: vars.typography.heading.weight,
  lineHeight: vars.typography.heading.lineHeight,
  letterSpacing: vars.typography.heading.tracking,
  textAlign: 'center'
})

export const subtitle = style({
  margin: '0',
  color: vars.color.text.secondary,
  overflowWrap: 'anywhere',
  textAlign: 'center'
})

export const licence = style({
  margin: '0',
  color: vars.color.text.secondary,
  overflowWrap: 'anywhere',
  textAlign: 'center'
})

export const hint = style({
  margin: '0',
  color: vars.color.text.secondary,
  overflowWrap: 'anywhere'
})

export const error = style({
  margin: '0',
  padding: '12px 16px',
  borderRadius: vars.radius.sm,
  overflowWrap: 'anywhere',
  background: vars.color.danger.soft,
  color: vars.color.danger.onSoft
})

export const success = style({
  margin: '0',
  padding: '12px 16px',
  borderRadius: vars.radius.sm,
  overflowWrap: 'anywhere',
  background: vars.color.selection.bg,
  color: vars.color.selection.fg
})

export const warning = style({
  margin: '0',
  padding: '12px 16px',
  borderRadius: vars.radius.sm,
  overflowWrap: 'anywhere',
  display: 'flex',
  flexDirection: 'column',
  gap: '8px',
  background: vars.color.highlight.soft,
  color: vars.color.highlight.onSoft
})

export const warningText = style({
  margin: '0'
})

export const actions = style({
  display: 'flex',
  alignItems: 'center',
  justifyContent: 'flex-end',
  flexWrap: 'wrap',
  gap: '8px',
  marginTop: '8px',
  width: '100%',
  '@media': {
    '(max-width: 599.98px)': {
      flexDirection: 'column',
      alignItems: 'stretch'
    }
  }
})

export const divider = style({
  display: 'flex',
  alignItems: 'center',
  gap: '12px',
  color: vars.color.text.secondary,
  selectors: {
    '&::before': {
      content: "''",
      flex: '1',
      borderTop: `1px solid ${vars.color.text.secondary}`
    },
    '&::after': {
      content: "''",
      flex: '1',
      borderTop: `1px solid ${vars.color.text.secondary}`
    }
  }
})

export const setupLink = style({
  paddingBlock: '4px',
  color: vars.color.accent.solid,
  textAlign: 'center',
  textDecoration: 'none',
  minHeight: '40px',
  display: 'flex',
  alignItems: 'center',
  justifyContent: 'center',
  overflowWrap: 'anywhere',
  selectors: {
    '&:hover': {
      textDecoration: 'underline'
    }
  },
  '@media': {
    '(max-width: 599.98px)': {
      minHeight: '44px'
    }
  }
})

export const licenceLink = style({
  color: vars.color.accent.solid
})

export const steps = style({
  display: 'flex',
  gap: '8px',
  flexWrap: 'wrap',
  margin: '0',
  padding: '8px 0',
  listStyle: 'none',
  color: vars.color.text.secondary
})

export const step = style({
  padding: '8px 12px',
  borderRadius: vars.radius.sm,
  border: `1px solid ${vars.color.border.subtle}`
})

export const stepActive = style({
  color: vars.color.accent.solid,
  borderColor: vars.color.accent.solid,
  fontWeight: '600'
})

export const pathRow = style({
  display: 'flex',
  gap: '8px',
  alignItems: 'center',
  '@media': {
    '(max-width: 599.98px)': {
      alignItems: 'stretch',
      flexDirection: 'column'
    }
  }
})

export const pathField = style({
  flex: '1 1 auto',
  minWidth: '0'
})

export const strength = style({
  display: 'flex',
  alignItems: 'center',
  gap: '8px',
  marginTop: '-8px'
})

export const strengthBar = style({
  flex: '1'
})

export const strengthLabel = style({
  flex: '0 0 auto',
  minWidth: '48px',
  color: vars.color.text.secondary
})

export const pathButton = style({
  '@media': {
    '(max-width: 599.98px)': {
      width: '100%'
    }
  }
})

export const action = style({
  '@media': {
    '(max-width: 599.98px)': {
      flex: '1 1 8rem',
      maxWidth: '100%'
    }
  }
})

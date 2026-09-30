import { style } from '@vanilla-extract/css'
import { buttonMaxWidth, buttonMinHeight, buttonWidth } from '../../ui/Button.css'
import { scaleUp, vars } from '../../ui/theme.css'

export const page = style({
  minHeight: '100dvh',
  minWidth: '0',
  overflowY: 'auto',
  display: 'flex',
  alignItems: 'center',
  justifyContent: 'center',
  padding: vars.layout.pagePad,
  background: `radial-gradient(circle at 20% 15%, rgba(${vars.color.primary}, 0.17), transparent 24rem), radial-gradient(circle at 85% 80%, rgba(${vars.color.secondary}, 0.12), transparent 28rem), ${vars.surface.page}`,
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
  borderRadius: vars.radius.auth,
  background: vars.surface.raised,
  border: `1px solid ${vars.outline.variant}`,
  boxShadow: vars.shadow.level1,
  animation: `${scaleUp} 240ms cubic-bezier(0.2, 0, 0, 1)`,
  '@media': {
    '(max-width: 599.98px)': {
      gap: '14px',
      padding: '20px 16px',
      borderRadius: vars.radius.auth,
      vars: {
        [buttonMinHeight]: '44px'
      }
    }
  }
})

export const login = style({
  width: 'min(100%, 36rem)',
  marginInline: 'auto',
  borderRadius: vars.radius.large,
  boxShadow: 'none'
})

export const title = style({
  minWidth: '0',
  margin: '0',
  overflowWrap: 'anywhere',
  fontSize: vars.typescale.headlineSmall.size,
  fontWeight: vars.typescale.headlineSmall.weight,
  lineHeight: vars.typescale.headlineSmall.lineHeight,
  letterSpacing: vars.typescale.headlineSmall.tracking,
  textAlign: 'center'
})

export const subtitle = style({
  margin: '0',
  color: vars.content.secondary,
  overflowWrap: 'anywhere',
  textAlign: 'center'
})

export const licence = style({
  margin: '0',
  color: vars.content.secondary,
  overflowWrap: 'anywhere',
  textAlign: 'center'
})

export const hint = style({
  margin: '0',
  color: vars.content.secondary,
  overflowWrap: 'anywhere'
})

export const error = style({
  margin: '0',
  padding: '12px 16px',
  borderRadius: vars.radius.small,
  overflowWrap: 'anywhere',
  background: vars.state.error,
  color: vars.state.errorContent
})

export const success = style({
  margin: '0',
  padding: '12px 16px',
  borderRadius: vars.radius.small,
  overflowWrap: 'anywhere',
  background: vars.state.selection,
  color: vars.state.selectionContent
})

export const warning = style({
  margin: '0',
  padding: '12px 16px',
  borderRadius: vars.radius.small,
  overflowWrap: 'anywhere',
  display: 'flex',
  flexDirection: 'column',
  gap: '8px',
  background: vars.state.warning,
  color: vars.state.warningContent
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
      vars: {
        [buttonWidth]: '100%',
        [buttonMaxWidth]: '100%'
      }
    }
  }
})

export const divider = style({
  display: 'flex',
  alignItems: 'center',
  gap: '12px',
  color: vars.content.secondary,
  selectors: {
    '&::before': {
      content: "''",
      flex: '1',
      borderTop: `1px solid ${vars.content.secondary}`
    },
    '&::after': {
      content: "''",
      flex: '1',
      borderTop: `1px solid ${vars.content.secondary}`
    }
  }
})

export const setupLink = style({
  paddingBlock: '4px',
  color: `rgb(${vars.color.primary})`,
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
  color: `rgb(${vars.color.primary})`
})

export const steps = style({
  display: 'flex',
  gap: '8px',
  flexWrap: 'wrap',
  margin: '0',
  padding: '8px 0',
  listStyle: 'none',
  color: vars.content.secondary
})

export const step = style({
  padding: '8px 12px',
  borderRadius: vars.radius.small,
  border: `1px solid ${vars.outline.variant}`
})

export const stepActive = style({
  color: `rgb(${vars.color.primary})`,
  borderColor: `rgb(${vars.color.primary})`,
  fontWeight: '600'
})

export const pathRow = style({
  display: 'flex',
  gap: '8px',
  alignItems: 'center',
  '@media': {
    '(max-width: 599.98px)': {
      alignItems: 'stretch',
      flexDirection: 'column',
      vars: {
        [buttonWidth]: '100%'
      }
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
  color: vars.content.secondary
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

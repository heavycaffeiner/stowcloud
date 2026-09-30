import { style } from '@vanilla-extract/css'
import { buttonMinHeight, buttonWidth } from '../../../ui/Button.css'
import { vars } from '../../../ui/theme.css'

export const root = style({
  display: 'flex',
  alignItems: 'center',
  justifyContent: 'center',
  minHeight: '100dvh',
  minWidth: '0',
  overflowY: 'auto',
  padding: vars.layout.pagePad,
  '@media': {
    '(max-width: 599.98px)': {
      alignItems: 'flex-start',
      paddingBlock: '16px'
    }
  }
})

export const card = style({
  display: 'flex',
  flexDirection: 'column',
  gap: '16px',
  width: 'min(640px, 100%)',
  minWidth: '0',
  padding: '24px',
  borderRadius: '24px',
  background: vars.surface.container,
  boxShadow: '0 8px 24px rgb(0 0 0 / 0.2)',
  '@media': {
    '(max-width: 599.98px)': {
      gap: '14px',
      padding: '20px 16px',
      borderRadius: '20px'
    }
  }
})

export const title = style({
  margin: '0',
  overflowWrap: 'anywhere',
  fontSize: vars.typescale.headlineSmall.size,
  fontWeight: vars.typescale.headlineSmall.weight,
  lineHeight: vars.typescale.headlineSmall.lineHeight,
  letterSpacing: vars.typescale.headlineSmall.tracking
})

export const subtitle = style({
  margin: '0',
  color: vars.content.secondary,
  overflowWrap: 'anywhere'
})

export const hint = style({
  margin: '0',
  color: vars.content.secondary,
  overflowWrap: 'anywhere'
})

export const form = style({
  display: 'flex',
  flexDirection: 'column',
  gap: '12px'
})

export const actions = style({
  display: 'flex',
  gap: '8px',
  flexWrap: 'wrap',
  alignItems: 'center',
  '@media': {
    '(max-width: 599.98px)': {
      vars: {
        [buttonMinHeight]: '44px',
        [buttonWidth]: '100%'
      }
    }
  }
})

export const label = style({
  fontSize: vars.typescale.labelLarge.size,
  fontWeight: vars.typescale.labelLarge.weight,
  lineHeight: vars.typescale.labelLarge.lineHeight,
  letterSpacing: vars.typescale.labelLarge.tracking
})

export const textarea = style({
  width: '100%',
  minWidth: '0',
  padding: '8px 12px',
  border: 'none',
  borderRadius: vars.shape.cornerSmall,
  background: `rgb(${vars.color.surfaceContainerHighest})`,
  color: vars.content.primary,
  font: 'inherit',
  selectors: {
    '&:focus-visible': {
      outline: `${vars.focusRing.width} solid ${vars.state.focus}`,
      outlineOffset: vars.focusRing.offset
    }
  },
  minHeight: '18rem',
  fontFamily: 'ui-monospace, monospace',
  maxWidth: '100%',
  resize: 'vertical'
})

export const facts = style({
  display: 'grid',
  gridTemplateColumns: 'auto 1fr',
  gap: '4px 16px',
  margin: '0',
  '@media': {
    '(max-width: 599.98px)': {
      gridTemplateColumns: 'minmax(0, 1fr)',
      gap: '2px'
    }
  }
})

export const factValue = style({
  minWidth: '0',
  margin: '0',
  overflowWrap: 'anywhere'
})

export const factLabel = style({
  color: vars.content.secondary,
  '@media': {
    '(max-width: 599.98px)': {
      selectors: {
        [`${factValue} + &`]: {
          marginTop: '8px'
        }
      }
    }
  }
})

export const banner = style({
  margin: '0',
  padding: '12px 16px',
  borderRadius: '8px',
  overflowWrap: 'anywhere',
  background: vars.state.warning,
  color: vars.state.warningContent
})

export const warning = style({
  margin: '0',
  padding: '12px 16px',
  borderRadius: '8px',
  overflowWrap: 'anywhere',
  background: vars.state.warning,
  color: vars.state.warningContent
})

export const error = style({
  margin: '0',
  padding: '12px 16px',
  borderRadius: '8px',
  overflowWrap: 'anywhere',
  background: vars.state.error,
  color: vars.state.errorContent
})

export const ok = style({
  margin: '0',
  color: vars.color.primary,
  overflowWrap: 'anywhere'
})

export const action = style({
  '@media': {
    '(max-width: 599.98px)': {
      flex: '1 1 12rem'
    }
  }
})

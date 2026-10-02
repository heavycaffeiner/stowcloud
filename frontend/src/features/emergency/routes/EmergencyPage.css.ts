import { style } from '@vanilla-extract/css'
import { buttonMinHeight, buttonWidth } from '../../../ui/Button.css'
import { vars } from '@/shared/theme'

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
  background: vars.color.surface.container,
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
  fontSize: vars.typography.heading.size,
  fontWeight: vars.typography.heading.weight,
  lineHeight: vars.typography.heading.lineHeight,
  letterSpacing: vars.typography.heading.tracking
})

export const subtitle = style({
  margin: '0',
  color: vars.color.text.secondary,
  overflowWrap: 'anywhere'
})

export const hint = style({
  margin: '0',
  color: vars.color.text.secondary,
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
  fontSize: vars.typography.label.size,
  fontWeight: vars.typography.label.weight,
  lineHeight: vars.typography.label.lineHeight,
  letterSpacing: vars.typography.label.tracking
})

export const textarea = style({
  width: '100%',
  minWidth: '0',
  padding: '8px 12px',
  border: 'none',
  borderRadius: vars.radius.sm,
  background: vars.color.surface.fill,
  color: vars.color.text.primary,
  font: 'inherit',
  selectors: {
    '&:focus-visible': {
      outline: `${vars.focusRing.width} solid ${vars.color.focus}`,
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
  color: vars.color.text.secondary,
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
  background: vars.color.highlight.soft,
  color: vars.color.highlight.onSoft
})

export const warning = style({
  margin: '0',
  padding: '12px 16px',
  borderRadius: '8px',
  overflowWrap: 'anywhere',
  background: vars.color.highlight.soft,
  color: vars.color.highlight.onSoft
})

export const error = style({
  margin: '0',
  padding: '12px 16px',
  borderRadius: '8px',
  overflowWrap: 'anywhere',
  background: vars.color.danger.soft,
  color: vars.color.danger.onSoft
})

export const ok = style({
  margin: '0',
  color: vars.color.accent.solid,
  overflowWrap: 'anywhere'
})

export const action = style({
  '@media': {
    '(max-width: 599.98px)': {
      flex: '1 1 12rem'
    }
  }
})

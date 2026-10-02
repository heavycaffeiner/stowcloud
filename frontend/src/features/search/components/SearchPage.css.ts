import { style } from '@vanilla-extract/css'
import { vars } from '../../../ui/theme.css'

export const root = style({
  display: 'flex',
  minHeight: '0',
  height: '100%',
  minWidth: '0',
  overflow: 'hidden',
  padding: vars.layout.pagePad
})

export const inner = style({
  display: 'flex',
  flexDirection: 'column',
  gap: '12px',
  width: '100%',
  minWidth: '0',
  minHeight: '0'
})

export const header = style({
  display: 'flex',
  alignItems: 'center',
  gap: '8px'
})

export const title = style({
  margin: '0',
  fontSize: vars.typescale.headlineSmall.size,
  fontWeight: vars.typescale.headlineSmall.weight,
  lineHeight: vars.typescale.headlineSmall.lineHeight,
  letterSpacing: vars.typescale.headlineSmall.tracking
})

export const routeBack = style({
  selectors: {
    '&:focus-visible': {
      outline: `${vars.focusRing.width} solid ${vars.state.focus}`,
      outlineOffset: vars.focusRing.offset
    }
  },
  display: 'inline-flex',
  alignItems: 'center',
  justifyContent: 'center',
  width: '40px',
  height: '40px',
  padding: '0',
  border: '0',
  borderRadius: '50%',
  background: 'transparent',
  color: 'inherit',
  cursor: 'pointer',
  flex: 'none',
  '@media': {
    '(max-width: 599.98px)': {
      width: '44px',
      height: '44px'
    }
  }
})

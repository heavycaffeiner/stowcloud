import { style } from '@vanilla-extract/css'
import { vars } from '../../../ui/theme.css'

export const root = style({
  display: 'flex',
  flexDirection: 'column',
  gap: '8px',
  minInlineSize: '0',
  width: 'min(420px, 80vw)',
  maxWidth: '100%'
})

export const nav = style({
  display: 'flex',
  alignItems: 'center',
  gap: '8px'
})

export const here = style({
  margin: '0',
  overflowWrap: 'anywhere',
  color: vars.content.secondary
})

export const body = style({
  minHeight: '0',
  minWidth: '0',
  maxHeight: '40vh',
  overflowY: 'auto',
  border: 'none',
  background: `rgb(${vars.color.surfaceContainerHigh})`,
  borderRadius: '8px'
})

export const status = style({
  margin: '0',
  padding: '16px',
  color: vars.content.secondary
})

export const entries = style({
  listStyle: 'none',
  margin: '0',
  padding: '4px'
})

export const entry = style({
  display: 'flex',
  alignItems: 'center',
  gap: '8px',
  width: '100%',
  minHeight: vars.control.min,
  padding: '8px',
  border: 'none',
  borderRadius: '4px',
  background: 'none',
  color: 'inherit',
  textAlign: 'left',
  selectors: {
    'button&': {
      cursor: 'pointer'
    },
    'button&:hover': {
      background: vars.surface.raised
    }
  }
})

export const entryName = style({
  minWidth: '0',
  overflowWrap: 'anywhere'
})

export const entrySelected = style({
  background: vars.state.selection,
  color: vars.state.selectionContent
})

export const entryDisabled = style({
  color: vars.content.secondary,
  opacity: '0.6'
})

import { createVar, fallbackVar, style } from '@vanilla-extract/css'
import { vars } from './theme.css'

export const buttonMinHeight = createVar()

export const buttonWidth = createVar()

export const buttonMaxWidth = createVar()

export const wrap = style({
  display: 'inline-flex',
  alignItems: 'center',
  justifyContent: 'center',
  verticalAlign: 'middle',
  transition: 'transform 120ms cubic-bezier(0.2, 0, 0, 1), box-shadow 150ms cubic-bezier(0.2, 0, 0, 1)',
  selectors: {
    '&:active': {
      transform: 'scale(0.97)'
    }
  }
})

export const button = style({
  display: 'inline-flex',
  minHeight: fallbackVar(buttonMinHeight, 'auto'),
  width: fallbackVar(buttonWidth, 'auto'),
  maxWidth: fallbackVar(buttonMaxWidth, 'none'),
  selectors: {
    '&[variant="outlined"]': {
      border: 'none !important',
      backgroundColor: `rgb(${vars.color.surfaceContainerHigh}) !important`,
      color: `rgb(${vars.color.onSurface}) !important`,
      transition:
        'background-color 140ms ease, transform 120ms cubic-bezier(0.2, 0, 0, 1), box-shadow 150ms ease !important'
    },
    '&[variant="outlined"]:hover': {
      backgroundColor: `rgb(${vars.color.surfaceContainerHighest}) !important`,
      color: `rgb(${vars.color.primary}) !important`
    },
    '&[variant="outlined"][disabled]': {
      backgroundColor: `rgba(${vars.color.onSurface}, 0.12) !important`,
      color: `rgba(${vars.color.onSurface}, 0.38) !important`
    },
    '&::part(button)': {
      alignItems: 'center',
      justifyContent: 'center'
    },
    '&::part(label)': {
      display: 'flex',
      alignItems: 'center',
      alignSelf: 'stretch',
      lineHeight: vars.typescale.labelLarge.lineHeight
    },
    '&::part(button):focus-visible': {
      outline: `${vars.focusRing.width} solid ${vars.state.focus}`,
      outlineOffset: vars.focusRing.offset
    },
    '&:active': {
      transform: 'scale(0.97)'
    }
  },
  transition: 'transform 120ms cubic-bezier(0.2, 0, 0, 1), box-shadow 150ms cubic-bezier(0.2, 0, 0, 1)'
})

export const square = style({
  selectors: {
    'mdui-button&': {
      inlineSize: vars.control.min,
      minInlineSize: vars.control.min,
      minBlockSize: vars.control.min,
      paddingInline: '0'
    },
    'mdui-button&::part(button)': {
      paddingInline: '0',
      justifyContent: 'center'
    },
    'mdui-button&::part(label)': {
      display: 'none'
    }
  }
})

export const slot = style({
  display: 'inline-flex',
  alignItems: 'center',
  justifyContent: 'center',
  verticalAlign: 'middle'
})

export const loadingLabel = style({
  visibility: 'hidden'
})

export const danger = style({
  display: 'inline-flex',
  alignItems: 'center',
  verticalAlign: 'middle',
  vars: {
    [vars.color.primary]: vars.color.error,
    [vars.color.onPrimary]: vars.color.onError,
    [vars.color.primaryContainer]: vars.color.errorContainer,
    [vars.color.onPrimaryContainer]: vars.color.onErrorContainer,
    [vars.color.secondary]: vars.color.error,
    [vars.color.onSecondary]: vars.color.onError,
    [vars.color.secondaryContainer]: vars.color.errorContainer,
    [vars.color.onSecondaryContainer]: vars.color.onErrorContainer,
    [vars.color.outline]: vars.color.error,
    [vars.color.outlineVariant]: vars.color.error,
    [vars.color.onSurfaceVariant]: vars.color.error
  }
})

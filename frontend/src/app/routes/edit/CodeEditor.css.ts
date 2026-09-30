import { createVar, style } from '@vanilla-extract/css'
import { vars } from '../../../ui/theme.css'

export const codeVars = {
  activeLine: createVar(),
  caret: createVar(),
  comment: createVar(),
  definition: createVar(),
  foreground: createVar(),
  gutter: createVar(),
  gutterText: createVar(),
  heading: createVar(),
  invalid: createVar(),
  keyword: createVar(),
  link: createVar(),
  number: createVar(),
  selection: createVar(),
  string: createVar(),
  type: createVar()
}

export const root = style({
  flex: '1',
  minHeight: '0',
  overflow: 'hidden',
  border: `1px solid rgb(${vars.color.outlineVariant})`,
  borderRadius: vars.shape.cornerLarge,
  background: `rgb(${vars.color.surface})`,
  boxShadow: `0 1px 3px rgb(${vars.color.shadow} / .08)`,
  transition: 'border-color 120ms ease, box-shadow 120ms ease',
  vars: {
    [codeVars.activeLine]: `rgb(${vars.color.primary} / .06)`,
    [codeVars.caret]: `rgb(${vars.color.primary})`,
    [codeVars.comment]: `rgb(${vars.color.onSurfaceVariant})`,
    [codeVars.definition]: `rgb(${vars.color.tertiary})`,
    [codeVars.foreground]: `rgb(${vars.color.onSurface})`,
    [codeVars.gutter]: `rgb(${vars.color.surfaceContainer})`,
    [codeVars.gutterText]: `rgb(${vars.color.onSurfaceVariant})`,
    [codeVars.heading]: `rgb(${vars.color.primary})`,
    [codeVars.invalid]: `rgb(${vars.color.error})`,
    [codeVars.keyword]: `rgb(${vars.color.primary})`,
    [codeVars.link]: `rgb(${vars.color.primary})`,
    [codeVars.number]: `rgb(${vars.color.secondary})`,
    [codeVars.selection]: `rgb(${vars.color.primary} / .18)`,
    [codeVars.string]: `rgb(${vars.color.tertiary})`,
    [codeVars.type]: `rgb(${vars.color.secondary})`
  },
  selectors: {
    '&:focus-within': {
      borderColor: `rgb(${vars.color.primary})`,
      boxShadow: `0 0 0 2px rgb(${vars.color.primary} / .16)`
    }
  },
  '@media': {
    '(max-width: 600px)': {
      border: '0',
      borderRadius: '0',
      boxShadow: 'none',
      selectors: {
        '&:focus-within': {
          boxShadow: `inset 0 2px 0 rgb(${vars.color.primary})`
        }
      }
    },
    '(prefers-reduced-motion: reduce)': {
      transition: 'none'
    }
  }
})

export const status = style({
  margin: '0',
  padding: '24px',
  color: `rgb(${vars.color.onSurfaceVariant})`
})

export const statusError = style({
  color: `rgb(${vars.color.error})`
})

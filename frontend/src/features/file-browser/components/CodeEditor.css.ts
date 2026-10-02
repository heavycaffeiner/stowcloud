import { createVar, style } from '@vanilla-extract/css'
import { vars } from '@/shared/theme'

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
  border: `1px solid ${vars.color.border.subtle}`,
  borderRadius: vars.radius.lg,
  background: vars.color.surface.page,
  boxShadow: `0 1px 3px color-mix(in srgb, ${vars.color.shadow} 8%, transparent)`,
  transition: 'border-color 120ms ease, box-shadow 120ms ease',
  vars: {
    [codeVars.activeLine]: `color-mix(in srgb, ${vars.color.accent.solid} 6%, transparent)`,
    [codeVars.caret]: vars.color.accent.solid,
    [codeVars.comment]: vars.color.text.secondary,
    [codeVars.definition]: vars.color.highlight.solid,
    [codeVars.foreground]: vars.color.text.primary,
    [codeVars.gutter]: vars.color.surface.raised,
    [codeVars.gutterText]: vars.color.text.secondary,
    [codeVars.heading]: vars.color.accent.solid,
    [codeVars.invalid]: vars.color.danger.solid,
    [codeVars.keyword]: vars.color.accent.solid,
    [codeVars.link]: vars.color.accent.solid,
    [codeVars.number]: vars.color.neutral.solid,
    [codeVars.selection]: `color-mix(in srgb, ${vars.color.accent.solid} 18%, transparent)`,
    [codeVars.string]: vars.color.highlight.solid,
    [codeVars.type]: vars.color.neutral.solid
  },
  selectors: {
    '&:focus-within': {
      borderColor: vars.color.accent.solid,
      boxShadow: `0 0 0 2px color-mix(in srgb, ${vars.color.accent.solid} 16%, transparent)`
    }
  },
  '@media': {
    '(max-width: 600px)': {
      border: '0',
      borderRadius: '0',
      boxShadow: 'none',
      selectors: {
        '&:focus-within': {
          boxShadow: `inset 0 2px 0 ${vars.color.accent.solid}`
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
  color: vars.color.text.secondary
})

export const statusError = style({
  color: vars.color.danger.solid
})

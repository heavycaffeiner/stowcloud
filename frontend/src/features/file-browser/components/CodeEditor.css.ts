import { createVar, style } from '@vanilla-extract/css'
import { media, vars } from '@/shared/theme'

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
  flex: 1,
  minHeight: 0,
  overflow: 'hidden',
  border: `${vars.stroke.thin} solid ${vars.color.border.subtle}`,
  borderRadius: vars.radius.lg,
  background: vars.color.surface.page,
  boxShadow: vars.elevation.sm,
  transition: `border-color ${vars.motion.short} ${vars.motion.easing}, box-shadow ${vars.motion.short} ${vars.motion.easing}`,
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
      boxShadow: `0 0 0 ${vars.stroke.thick} color-mix(in srgb, ${vars.color.accent.solid} 16%, transparent)`
    }
  },
  '@media': {
    [media.compact]: {
      border: 0,
      borderRadius: 0,
      boxShadow: 'none',
      selectors: {
        '&:focus-within': {
          boxShadow: `inset 0 ${vars.stroke.thick} 0 ${vars.color.accent.solid}`
        }
      }
    }
  }
})

export const status = style({
  margin: 0,
  padding: vars.space.xl,
  color: vars.color.text.secondary
})

export const statusError = style({
  color: vars.color.danger.solid
})

import { globalStyle, style } from '@vanilla-extract/css'
import { media, typography, vars } from '@/shared/theme'

// The Mantine input family (text fields and selects) shares one look.

export const root = style({
  display: 'flex',
  flexDirection: 'column',
  gap: vars.space.xs,
  inlineSize: '100%',
  minInlineSize: 0,
  maxInlineSize: vars.layout.form
})

export const label = style({
  ...typography('label'),
  color: vars.color.text.primary
})

// An input that shares its line with an action, so the action lines up with the input box and not the label.
export const inputRow = style({
  display: 'flex',
  alignItems: 'center',
  gap: vars.space.sm,
  '@media': { [media.compact]: { flexDirection: 'column', alignItems: 'flex-start' } }
})

// A transparent fill reads the same on every surface the field lands on.
export const wrapper = style({
  vars: {
    '--input-height-sm': vars.density.control,
    '--input-radius': vars.radius.sm,
    '--input-padding': vars.space.md,
    '--input-bg': 'transparent',
    '--input-bd': vars.color.border.strong,
    '--input-bd-focus': vars.color.accent.solid
  },
  selectors: {
    '&[data-error]': { vars: { '--input-bd': vars.color.danger.solid, '--input-bd-focus': vars.color.danger.solid } },
    [`${inputRow} > &`]: { flex: '1 1 auto', minWidth: 0 }
  },
  '@media': { [media.compact]: { selectors: { [`${inputRow} > &`]: { alignSelf: 'stretch' } } } }
})

globalStyle(`${inputRow} > :not(${wrapper})`, { flex: 'none' })

// The focused border doubles in width, so focus never rests on a color change alone.
export const input = style({
  ...typography('bodyLarge'),
  color: vars.color.text.primary,
  selectors: {
    '&:focus': { boxShadow: `inset 0 0 0 ${vars.stroke.thin} var(--input-bd-focus)` },
    '&::placeholder': { color: vars.color.text.secondary }
  }
})

export const description = style({
  ...typography('bodySmall'),
  color: vars.color.text.secondary
})

export const error = style({
  ...typography('bodySmall'),
  color: vars.color.danger.solid,
  overflowWrap: 'anywhere'
})

export const classNames = { label, wrapper, input, description, error }

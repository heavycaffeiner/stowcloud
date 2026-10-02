import { style } from '@vanilla-extract/css'
import { typography, vars } from '@/shared/theme'

// The Mantine input family (text fields and selects) shares one look.

export const root = style({
  display: 'flex',
  flexDirection: 'column',
  gap: vars.space.xs,
  inlineSize: '100%',
  minInlineSize: '0',
  maxInlineSize: '35rem'
})

export const label = style({
  ...typography('label'),
  color: vars.color.text.primary
})

// A transparent fill reads the same on every surface the field lands on.
export const wrapper = style({
  vars: {
    '--input-height-sm': vars.density.control,
    '--input-radius': vars.radius.sm,
    '--input-bg': 'transparent',
    '--input-bd': vars.color.border.strong,
    '--input-bd-focus': vars.color.accent.solid
  },
  selectors: {
    '&[data-error]': { vars: { '--input-bd': vars.color.danger.solid, '--input-bd-focus': vars.color.danger.solid } }
  }
})

// The focused border doubles in width, so focus never rests on a color change alone.
export const input = style({
  ...typography('bodyLarge'),
  color: vars.color.text.primary,
  paddingInline: vars.space.md,
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

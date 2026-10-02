import {
  createTheme,
  defaultVariantColorsResolver,
  type CSSVariablesResolver,
  type MantineColorsTuple,
  type VariantColorResolverResult,
  type VariantColorsResolver
} from '@mantine/core'
import { vars } from './contract.css'
import { breakpoints } from './media'
import { palette } from './palette'
import { focusRing, pressable } from './utilities.css'

// Mantine indexes a ramp from lightest (0) to darkest (9).
const ramp = (tones: Record<number, string>, steps: readonly number[]): MantineColorsTuple =>
  steps.map((tone) => tones[tone]) as unknown as MantineColorsTuple

const color = vars.color

// A hover or press tint of `fg` over `bg`, the way a state layer sits over a surface.
const stateLayer = (bg: string, fg: string): string => `color-mix(in srgb, ${bg}, ${fg} 8%)`

// The tonal pair for the accent is the quiet selection color, so a tonal button never competes with a filled one.
const tones = {
  accent: {
    solid: color.accent.solid,
    onSolid: color.accent.onSolid,
    tonal: color.selection.bg,
    onTonal: color.selection.fg
  },
  danger: {
    solid: color.danger.solid,
    onSolid: color.danger.onSolid,
    tonal: color.danger.soft,
    onTonal: color.danger.onSoft
  }
}

/** The Stowcloud button variants. Mantine's own variants still resolve for the components that use them. */
export type StowVariant = 'filled' | 'tonal' | 'outlined' | 'text' | 'standard'

const stowVariants: Record<StowVariant, (tone: (typeof tones)['accent']) => VariantColorResolverResult> = {
  filled: (tone) => ({
    background: tone.solid,
    hover: stateLayer(tone.solid, tone.onSolid),
    color: tone.onSolid,
    border: 'none'
  }),
  tonal: (tone) => ({
    background: tone.tonal,
    hover: stateLayer(tone.tonal, tone.onTonal),
    color: tone.onTonal,
    border: 'none'
  }),
  outlined: (tone) => ({
    background: color.surface.overlay,
    hover: color.surface.fill,
    hoverColor: tone.solid,
    color: color.text.primary,
    border: 'none'
  }),
  text: (tone) => ({
    background: 'transparent',
    hover: stateLayer('transparent', tone.solid),
    color: tone.solid,
    border: 'none'
  }),
  standard: () => ({
    background: 'transparent',
    hover: stateLayer('transparent', color.text.secondary),
    color: color.text.secondary,
    border: 'none'
  })
}

const isStowVariant = (variant: string): variant is StowVariant => Object.hasOwn(stowVariants, variant)

const variantColorResolver: VariantColorsResolver = (input) =>
  isStowVariant(input.variant)
    ? stowVariants[input.variant](input.color === 'danger' ? tones.danger : tones.accent)
    : defaultVariantColorsResolver(input)

export const mantineTheme = createTheme({
  white: palette.gray[100],
  black: palette.gray[0],
  colors: {
    accent: ramp(palette.blue, [95, 90, 80, 70, 60, 50, 40, 30, 20, 10]),
    gray: ramp(palette.gray, [98, 95, 92, 90, 80, 70, 50, 30, 20, 10]),
    dark: ramp(palette.gray, [80, 70, 50, 40, 30, 24, 17, 10, 6, 4])
  },
  primaryColor: 'accent',
  // Tone 40 on light surfaces and tone 80 on dark ones, the same tones the accent tokens use.
  primaryShade: { light: 6, dark: 2 },
  variantColorResolver,
  focusClassName: focusRing,
  activeClassName: pressable,
  respectReducedMotion: true,
  cursorType: 'pointer',
  fontFamily: vars.font.family,
  fontFamilyMonospace: vars.font.mono,
  headings: { fontFamily: vars.font.family },
  fontSizes: {
    xs: vars.typography.bodySmall.size,
    sm: vars.typography.body.size,
    md: vars.typography.bodyLarge.size,
    lg: vars.typography.titleLarge.size,
    xl: vars.typography.heading.size
  },
  lineHeights: {
    xs: vars.typography.bodySmall.lineHeight,
    sm: vars.typography.body.lineHeight,
    md: vars.typography.bodyLarge.lineHeight,
    lg: vars.typography.titleLarge.lineHeight,
    xl: vars.typography.heading.lineHeight
  },
  // Mantine's components size their padding from this scale, so it maps one step up to keep their proportions.
  spacing: { xs: vars.space.sm, sm: vars.space.md, md: vars.space.lg, lg: vars.space.xl, xl: vars.space.xxl },
  radius: { xs: vars.radius.xs, sm: vars.radius.sm, md: vars.radius.md, lg: vars.radius.lg, xl: vars.radius.xl },
  defaultRadius: 'sm',
  shadows: {
    xs: vars.elevation.sm,
    sm: vars.elevation.sm,
    md: vars.elevation.md,
    lg: vars.elevation.lg,
    xl: vars.elevation.xl
  },
  breakpoints: { sm: `${breakpoints.medium / 16}em`, md: `${breakpoints.wide / 16}em` }
})

// The Stowcloud tokens already switch with the color scheme, so both schemes point Mantine at the same names.
const schemeVariables = {
  '--mantine-color-body': color.surface.page,
  '--mantine-color-text': color.text.primary,
  '--mantine-color-bright': color.text.primary,
  '--mantine-color-dimmed': color.text.secondary,
  '--mantine-color-placeholder': color.text.secondary,
  '--mantine-color-error': color.danger.solid,
  '--mantine-color-anchor': color.accent.solid,
  '--mantine-color-default': color.surface.overlay,
  '--mantine-color-default-hover': color.surface.fill,
  '--mantine-color-default-color': color.text.primary,
  '--mantine-color-default-border': color.border.strong,
  '--mantine-color-disabled': `color-mix(in srgb, ${color.text.primary} 12%, transparent)`,
  '--mantine-color-disabled-color': `color-mix(in srgb, ${color.text.primary} 38%, transparent)`,
  '--mantine-color-disabled-border': `color-mix(in srgb, ${color.text.primary} 12%, transparent)`,
  '--mantine-primary-color-contrast': color.accent.onSolid
}

export const cssVariablesResolver: CSSVariablesResolver = () => ({
  variables: {
    '--mantine-primary-color-filled': color.accent.solid,
    '--mantine-primary-color-filled-hover': stateLayer(color.accent.solid, color.accent.onSolid),
    '--mantine-primary-color-light': color.accent.soft,
    '--mantine-primary-color-light-hover': stateLayer(color.accent.soft, color.accent.onSoft),
    '--mantine-primary-color-light-color': color.accent.onSoft
  },
  light: schemeVariables,
  dark: schemeVariables
})

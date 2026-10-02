import { assignVars, createGlobalTheme, globalStyle } from '@vanilla-extract/css'
import { vars } from './contract.css'
import { media } from './media'
import { palette } from './palette'

const { blue, slate, violet, red, gray, steel } = palette

// Solids sit at tone 40 on light surfaces and tone 80 on dark ones; soft fills at 90 and 30.
const lightColors = {
  accent: { solid: blue[40], onSolid: blue[100], soft: blue[90], onSoft: blue[10] },
  neutral: { solid: slate[40], onSolid: slate[100] },
  highlight: { solid: violet[40], soft: violet[90], onSoft: violet[10] },
  danger: { solid: red[40], onSolid: red[100], soft: red[90], onSoft: red[10] },
  selection: { bg: slate[90], fg: slate[10] },
  surface: {
    page: gray[99],
    sunken: gray[100],
    container: gray[96],
    raised: gray[94],
    overlay: gray[92],
    fill: gray[90],
    inverse: gray[20]
  },
  text: { primary: gray[10], secondary: steel[30], icon: '#475569', inverse: gray[95] },
  border: { strong: steel[50], subtle: steel[80] },
  focus: vars.color.neutral.solid,
  scrim: gray[0],
  shadow: gray[0]
}

const darkColors = {
  accent: { solid: blue[80], onSolid: blue[20], soft: blue[30], onSoft: blue[90] },
  neutral: { solid: slate[80], onSolid: slate[20] },
  highlight: { solid: violet[80], soft: violet[30], onSoft: violet[90] },
  danger: { solid: red[80], onSolid: red[20], soft: red[30], onSoft: red[90] },
  selection: { bg: slate[30], fg: slate[90] },
  surface: {
    page: gray[10],
    sunken: gray[4],
    container: gray[10],
    raised: gray[12],
    overlay: gray[17],
    fill: gray[22],
    inverse: gray[90]
  },
  text: { primary: gray[90], secondary: steel[80], icon: '#cbd5e1', inverse: gray[20] },
  border: { strong: steel[60], subtle: steel[30] },
  focus: vars.color.neutral.solid,
  scrim: gray[0],
  shadow: gray[0]
}

const typeRole = (size: string, lineHeight: string, weight: string, tracking: string) => ({
  size,
  lineHeight,
  weight,
  tracking
})

createGlobalTheme(':root', vars, {
  color: lightColors,
  font: {
    family: "'Google Sans Flex Variable', system-ui, -apple-system, BlinkMacSystemFont, 'Segoe UI', sans-serif",
    mono: 'ui-monospace, SFMono-Regular, Menlo, Consolas, monospace'
  },
  typography: {
    heading: typeRole('1.5rem', '2rem', '400', '0rem'),
    titleLarge: typeRole('1.375rem', '1.75rem', '400', '0rem'),
    title: typeRole('1rem', '1.5rem', '500', '0.009375rem'),
    titleSmall: typeRole('0.875rem', '1.25rem', '500', '0.00625rem'),
    bodyLarge: typeRole('1rem', '1.5rem', '400', '0.009375rem'),
    body: typeRole('0.875rem', '1.25rem', '400', '0.015625rem'),
    bodySmall: typeRole('0.75rem', '1rem', '400', '0.025rem'),
    label: typeRole('0.875rem', '1.25rem', '500', '0.00625rem'),
    labelSmall: typeRole('0.75rem', '1rem', '500', '0.03125rem'),
    caption: typeRole('0.6875rem', '1rem', '500', '0.03125rem')
  },
  space: { xxs: '2px', xs: '4px', sm: '8px', md: '12px', lg: '16px', xl: '24px', xxl: '32px' },
  radius: { xs: '4px', sm: '8px', md: '12px', lg: '16px', xl: '20px', full: '999px' },
  stroke: { thin: '1px', thick: '2px' },
  elevation: {
    sm: `0 1px 2px color-mix(in srgb, ${vars.color.shadow} 6%, transparent)`,
    md: `0 0.85px 3px 0 color-mix(in srgb, ${vars.color.shadow} 19%, transparent), 0 0.25px 1px 0 color-mix(in srgb, ${vars.color.shadow} 3.9%, transparent)`,
    lg: `0 8px 24px color-mix(in srgb, ${vars.color.shadow} 18%, transparent)`,
    xl: `0 16px 40px color-mix(in srgb, ${vars.color.shadow} 22%, transparent)`
  },
  density: { control: vars.density.controlDesktop, controlDesktop: '40px', controlCompact: '44px', row: '48px' },
  layout: {
    pagePad: '16px',
    contentPad: '24px',
    header: '56px',
    navBar: '4rem',
    navDrawer: '240px',
    navDrawerCollapsed: '68px'
  },
  focusRing: { width: '3px', offset: '2px', insetOffset: '-3px' },
  motion: { short: '120ms', medium: '300ms', easing: 'cubic-bezier(0.2, 0, 0, 1)' }
})

// Mantine writes the resolved scheme on <html>. Before it does, the system preference decides.
globalStyle(':root[data-mantine-color-scheme="dark"]', { vars: assignVars(vars.color, darkColors) })
globalStyle(':root:not([data-mantine-color-scheme])', {
  '@media': { [media.dark]: { vars: assignVars(vars.color, darkColors) } }
})

globalStyle(':root', {
  '@media': {
    [media.compact]: {
      vars: { [vars.layout.contentPad]: '16px', [vars.density.control]: vars.density.controlCompact }
    },
    [media.medium]: { vars: { [vars.layout.pagePad]: '24px' } },
    [media.wide]: { vars: { [vars.layout.pagePad]: '32px' } }
  }
})

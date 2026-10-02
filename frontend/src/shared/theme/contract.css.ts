import { createGlobalThemeContract } from '@vanilla-extract/css'

const kebab = (segment: string): string => segment.replace(/[A-Z]/g, (letter) => `-${letter.toLowerCase()}`)

const typeRole = { size: null, weight: null, lineHeight: null, tracking: null }

/** The Stowcloud design tokens. Styles and the Mantine theme both read these, never Mantine's own variables. */
export const vars = createGlobalThemeContract(
  {
    color: {
      accent: { solid: null, onSolid: null, soft: null, onSoft: null },
      neutral: { solid: null, onSolid: null },
      highlight: { solid: null, soft: null, onSoft: null },
      danger: { solid: null, onSolid: null, soft: null, onSoft: null },
      selection: { bg: null, fg: null },
      surface: { page: null, sunken: null, container: null, raised: null, overlay: null, fill: null, inverse: null },
      text: { primary: null, secondary: null, icon: null, inverse: null },
      border: { strong: null, subtle: null },
      focus: null,
      scrim: null,
      shadow: null
    },
    font: { family: null, mono: null },
    typography: {
      heading: typeRole,
      titleLarge: typeRole,
      title: typeRole,
      titleSmall: typeRole,
      bodyLarge: typeRole,
      body: typeRole,
      bodySmall: typeRole,
      label: typeRole,
      labelSmall: typeRole,
      caption: typeRole
    },
    space: { xxs: null, xs: null, sm: null, md: null, lg: null, xl: null, xxl: null },
    radius: { xs: null, sm: null, md: null, lg: null, xl: null, full: null },
    elevation: { sm: null, md: null, lg: null, xl: null },
    density: { control: null, controlDesktop: null, controlCompact: null, row: null },
    layout: {
      pagePad: null,
      contentPad: null,
      header: null,
      navBar: null,
      navDrawer: null,
      navDrawerCollapsed: null
    },
    focusRing: { width: null, offset: null, insetOffset: null },
    motion: { short: null, medium: null, easing: null }
  },
  (_value, path) => `stow-${path.map(kebab).join('-')}`
)

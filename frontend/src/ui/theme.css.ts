import {
  createGlobalTheme,
  createGlobalThemeContract,
  createThemeContract,
  createVar,
  globalStyle,
  keyframes
} from '@vanilla-extract/css'

const kebab = (segment: string): string => segment.replace(/[A-Z]/g, (c) => `-${c.toLowerCase()}`)

const typescale = { size: '', weight: '', lineHeight: '', tracking: '' }

// mdui.css declares these on :root. Colors are bare "r, g, b" triplets, so wrap them in rgb().
const mdui = createGlobalThemeContract(
  {
    color: {
      primary: '',
      primaryLight: '',
      primaryDark: '',
      onPrimary: '',
      primaryContainer: '',
      onPrimaryContainer: '',
      secondary: '',
      onSecondary: '',
      secondaryContainer: '',
      onSecondaryContainer: '',
      tertiary: '',
      tertiaryContainer: '',
      onTertiaryContainer: '',
      error: '',
      onError: '',
      errorContainer: '',
      onErrorContainer: '',
      surface: '',
      surfaceContainerLowest: '',
      surfaceContainerLow: '',
      surfaceContainer: '',
      surfaceContainerHigh: '',
      surfaceContainerHighest: '',
      onSurface: '',
      onSurfaceVariant: '',
      inverseSurface: '',
      inverseOnSurface: '',
      outline: '',
      outlineVariant: '',
      shadow: '',
      scrim: ''
    },
    typescale: {
      headlineSmall: typescale,
      titleLarge: typescale,
      titleMedium: typescale,
      titleSmall: typescale,
      bodyLarge: typescale,
      bodyMedium: typescale,
      bodySmall: typescale,
      labelLarge: typescale,
      labelMedium: typescale,
      labelSmall: typescale
    },
    shape: {
      cornerExtraSmall: '',
      cornerSmall: '',
      cornerMedium: '',
      cornerLarge: '',
      cornerExtraLarge: '',
      cornerFull: ''
    },
    motion: { durationMedium2: '', easingStandard: '' },
    elevation: { level2: '' }
  },
  (_value, path) => `mdui-${path.map(kebab).join('-')}`
)

const own = createThemeContract({
  surface: { page: null, container: null, raised: null, overlay: null },
  content: { primary: null, secondary: null, icon: null },
  outline: { default: null, variant: null },
  state: {
    selection: null,
    selectionContent: null,
    focus: null,
    warning: null,
    warningContent: null,
    error: null,
    errorContent: null
  },
  layout: {
    pagePad: null,
    navBarHeight: null,
    navDrawerWidth: null,
    navDrawerCollapsedWidth: null,
    headerHeight: null,
    rowHeight: null,
    contentPad: null
  },
  radius: { small: null, medium: null, large: null, auth: null, full: null, card: null, control: null },
  shadow: { level1: null, level3: null, level4: null, card: null },
  control: { min: null, minDesktop: null, minCompact: null },
  focusRing: { width: null, offset: null, insetOffset: null }
})

export const vars = { ...mdui, ...own }

// The upload tray writes its top edge here at runtime so floating bars can stay above it.
export const trayStackTop = createVar()

export const fadeInUp = keyframes({
  from: { opacity: '0', transform: 'translateY(8px)' },
  to: { opacity: '1', transform: 'translateY(0)' }
})

export const scaleUp = keyframes({
  from: { opacity: '0', transform: 'scale(0.96)' },
  to: { opacity: '1', transform: 'scale(1)' }
})

export const fadeIn = keyframes({
  from: { opacity: '0' },
  to: { opacity: '1' }
})

// Loaded after mdui.css, so these replace mdui's own :root defaults.
globalStyle(':root', {
  colorScheme: 'light dark',
  fontFamily: "'Google Sans Flex Variable', system-ui, -apple-system, BlinkMacSystemFont, 'Segoe UI', sans-serif",
  vars: {
    [mdui.color.primaryLight]: '14, 56, 94',
    [mdui.color.primaryDark]: '154, 203, 255',
    [mdui.typescale.labelSmall.lineHeight]: '1rem'
  }
})

createGlobalTheme(':root', own, {
  surface: {
    page: `rgb(${mdui.color.surface})`,
    container: `rgb(${mdui.color.surfaceContainerLow})`,
    raised: `rgb(${mdui.color.surfaceContainer})`,
    overlay: `rgb(${mdui.color.surfaceContainerHigh})`
  },
  content: {
    primary: `rgb(${mdui.color.onSurface})`,
    secondary: `rgb(${mdui.color.onSurfaceVariant})`,
    icon: '#475569'
  },
  outline: {
    default: `rgb(${mdui.color.outline})`,
    variant: `rgb(${mdui.color.outlineVariant})`
  },
  state: {
    selection: `rgb(${mdui.color.secondaryContainer})`,
    selectionContent: `rgb(${mdui.color.onSecondaryContainer})`,
    focus: `rgb(${mdui.color.secondary})`,
    warning: `rgb(${mdui.color.tertiaryContainer})`,
    warningContent: `rgb(${mdui.color.onTertiaryContainer})`,
    error: `rgb(${mdui.color.errorContainer})`,
    errorContent: `rgb(${mdui.color.onErrorContainer})`
  },
  layout: {
    pagePad: '16px',
    navBarHeight: '4rem',
    navDrawerWidth: '240px',
    navDrawerCollapsedWidth: '68px',
    headerHeight: '56px',
    rowHeight: '48px',
    contentPad: '24px'
  },
  radius: {
    small: '8px',
    medium: '12px',
    large: '16px',
    auth: '20px',
    full: '999px',
    card: own.radius.medium,
    control: own.radius.small
  },
  shadow: {
    level1: '0 1px 2px rgb(0 0 0 / 0.06)',
    level3: '0 8px 24px rgb(0 0 0 / 0.18)',
    level4: '0 16px 40px rgb(0 0 0 / 0.22)',
    card: own.shadow.level1
  },
  control: {
    min: own.control.minDesktop,
    minDesktop: '40px',
    minCompact: '44px'
  },
  focusRing: { width: '3px', offset: '2px', insetOffset: '-3px' }
})

globalStyle(':root', {
  '@media': {
    '(max-width: 599.98px)': {
      vars: { [own.layout.contentPad]: '16px', [own.control.min]: own.control.minCompact }
    }
  }
})

globalStyle(':root', {
  '@media': { '(min-width: 600px)': { vars: { [own.layout.pagePad]: '24px' } } }
})

globalStyle(':root', {
  '@media': { '(min-width: 905px)': { vars: { [own.layout.pagePad]: '32px' } } }
})

globalStyle('.mdui-theme-dark', {
  vars: { [own.content.icon]: '#cbd5e1' }
})

globalStyle(':root:not(.mdui-theme-light)', {
  '@media': { '(prefers-color-scheme: dark)': { vars: { [own.content.icon]: '#cbd5e1' } } }
})

globalStyle('*', {
  boxSizing: 'border-box',
  WebkitTapHighlightColor: 'transparent'
})

globalStyle('html, body, #root', {
  minHeight: '100%',
  margin: '0'
})

globalStyle('html', {
  height: '100%'
})

globalStyle('body', {
  minWidth: '320px',
  background: own.surface.page,
  color: own.content.primary,
  fontFamily: 'inherit',
  fontFeatureSettings: "'tnum' 1",
  fontSize: mdui.typescale.bodyLarge.size,
  fontWeight: mdui.typescale.bodyLarge.weight,
  lineHeight: mdui.typescale.bodyLarge.lineHeight,
  letterSpacing: mdui.typescale.bodyLarge.tracking
})

globalStyle(':where(button, input, select, textarea)', {
  font: 'inherit'
})

globalStyle('svg', {
  display: 'inline-block',
  verticalAlign: 'middle',
  flexShrink: '0'
})

globalStyle('h1, h2, h3, h4, h5, h6', {
  margin: '0',
  wordBreak: 'keep-all'
})

globalStyle('h1', {
  fontSize: mdui.typescale.headlineSmall.size,
  fontWeight: mdui.typescale.headlineSmall.weight,
  lineHeight: mdui.typescale.headlineSmall.lineHeight,
  letterSpacing: mdui.typescale.headlineSmall.tracking
})

globalStyle('h2', {
  fontSize: mdui.typescale.titleLarge.size,
  fontWeight: mdui.typescale.titleLarge.weight,
  lineHeight: mdui.typescale.titleLarge.lineHeight,
  letterSpacing: mdui.typescale.titleLarge.tracking
})

globalStyle('h3', {
  fontSize: mdui.typescale.titleMedium.size,
  fontWeight: mdui.typescale.titleMedium.weight,
  lineHeight: mdui.typescale.titleMedium.lineHeight,
  letterSpacing: mdui.typescale.titleMedium.tracking
})

globalStyle('h4, h5, h6', {
  fontSize: mdui.typescale.titleSmall.size,
  fontWeight: mdui.typescale.titleSmall.weight,
  lineHeight: mdui.typescale.titleSmall.lineHeight,
  letterSpacing: mdui.typescale.titleSmall.tracking
})

globalStyle('*, *::before, *::after', {
  '@media': {
    '(prefers-reduced-motion: reduce)': {
      scrollBehavior: 'auto !important' as 'auto',
      animationDuration: '.01ms !important',
      animationIterationCount: '1 !important',
      transitionDuration: '.01ms !important',
      transform: 'none !important'
    }
  }
})

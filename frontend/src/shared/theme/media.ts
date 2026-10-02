// Media queries cannot read custom properties, so the layout breakpoints live here as plain values.
export const breakpoints = { medium: 600, wide: 905 } as const

export const media = {
  /** Phones: one column, touch-sized controls. */
  compact: `(max-width: ${breakpoints.medium - 0.02}px)`,
  /** Tablets and up. */
  medium: `(min-width: ${breakpoints.medium}px)`,
  /** Wide enough for the side navigation and the details pane. */
  wide: `(min-width: ${breakpoints.wide}px)`,
  /** The complement of `wide`, where navigation collapses into a bar and sheets. */
  notWide: `not all and (min-width: ${breakpoints.wide}px)`,
  reducedMotion: '(prefers-reduced-motion: reduce)',
  touch: '(hover: none), (pointer: coarse)',
  dark: '(prefers-color-scheme: dark)'
} as const

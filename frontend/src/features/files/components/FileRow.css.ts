import { style } from '@vanilla-extract/css'
import * as fileTableStyles from './FileTable.css'
import { vars } from '@/shared/theme'

export const root = style({
  display: 'flex',
  alignItems: 'center',
  height: vars.density.row,
  paddingInline: vars.layout.contentPad,
  cursor: 'pointer',
  borderBottom: 'none',
  color: vars.color.text.primary,
  fontSize: vars.typography.bodyLarge.size,
  fontWeight: vars.typography.bodyLarge.weight,
  lineHeight: vars.typography.bodyLarge.lineHeight,
  userSelect: 'none',
  WebkitUserSelect: 'none',
  transition: 'background-color 120ms ease, color 120ms ease',
  selectors: {
    '&:hover': {
      background: `color-mix(in srgb, ${vars.color.text.primary} 5%, transparent)`
    },
    [`${fileTableStyles.root}[data-density='compact'] &`]: {
      height: '40px'
    },
    [`${fileTableStyles.root}[data-density='comfortable'] &`]: {
      height: '48px'
    },
    [`${fileTableStyles.root}[data-density='spacious'] &`]: {
      height: '56px'
    },
    [`${fileTableStyles.root}${fileTableStyles.mobileRows} &`]: {
      height: '64px',
      paddingInline: '12px'
    },
    [`${fileTableStyles.mobileRows} &`]: {
      display: 'grid',
      gridTemplateColumns: '44px minmax(0, 1fr) 44px',
      alignItems: 'center'
    }
  },
  '@container': {
    [`${fileTableStyles.container} (max-width: 599.98px)`]: {
      paddingInline: '16px'
    }
  },
  '@media': {
    '(prefers-reduced-motion: reduce)': {
      transition: 'none',
      animation: 'none'
    }
  }
})

export const selected = style({
  background: vars.color.selection.bg,
  color: vars.color.selection.fg,
  selectors: {
    '&:hover': {
      background: `color-mix(in srgb, ${vars.color.selection.bg} 85%, ${vars.color.text.primary})`
    }
  }
})

export const focused = style({
  outline: 'none',
  selectors: {
    [`${fileTableStyles.root}:focus-visible &`]: {
      outline: `${vars.focusRing.width} solid ${vars.color.focus}`,
      outlineOffset: vars.focusRing.insetOffset
    }
  }
})

export const cell = style({
  display: 'flex',
  alignItems: 'center',
  gap: '8px',
  overflow: 'hidden'
})

export const cellSelect = style({
  flex: '0 0 40px',
  minHeight: vars.density.controlDesktop,
  overflow: 'visible',
  justifyContent: 'center',
  selectors: {
    [`${fileTableStyles.mobileRows} &`]: {
      gridColumn: '1',
      width: '44px',
      height: '44px',
      justifyContent: 'center',
      flex: 'none'
    }
  }
})

export const cellName = style({
  flex: '1 1 auto',
  minWidth: '0',
  selectors: {
    [`${fileTableStyles.mobileRows} &`]: {
      gridColumn: '2',
      minWidth: '0'
    }
  }
})

export const cellSize = style({
  flex: '0 0 112px',
  justifyContent: 'flex-end',
  color: vars.color.text.secondary,
  fontSize: vars.typography.body.size,
  lineHeight: vars.typography.body.lineHeight,
  '@container': {
    [`${fileTableStyles.container} (max-width: 599.98px)`]: {
      flexBasis: '88px'
    }
  },
  selectors: {
    [`${fileTableStyles.mobileRows} &`]: {
      display: 'none'
    }
  }
})

export const cellMtime = style({
  flex: `0 0 ${fileTableStyles.modifiedColumnWidth}`,
  justifyContent: 'flex-end',
  color: vars.color.text.secondary,
  fontSize: vars.typography.body.size,
  lineHeight: vars.typography.body.lineHeight,
  fontVariantNumeric: 'tabular-nums',
  whiteSpace: 'nowrap',
  '@container': {
    [`${fileTableStyles.container} (max-width: 599.98px)`]: {
      display: 'none'
    }
  },
  selectors: {
    [`${fileTableStyles.mobileRows} &`]: {
      display: 'none'
    }
  }
})

export const cellActions = style({
  flex: '0 0 40px',
  justifyContent: 'center',
  overflow: 'visible',
  selectors: {
    [`${fileTableStyles.mobileRows} &`]: {
      gridColumn: '3',
      width: '44px',
      height: '44px',
      justifyContent: 'center',
      flex: 'none'
    }
  }
})

export const iconBadge = style({
  display: 'inline-flex',
  alignItems: 'center',
  justifyContent: 'center',
  flex: 'none',
  width: '22px'
})

export const nameCopy = style({
  display: 'flex',
  flex: '1 1 auto',
  minWidth: '0',
  selectors: {
    [`${fileTableStyles.mobileRows} &`]: {
      flexDirection: 'column',
      justifyContent: 'center',
      gap: '1px',
      minWidth: '0'
    }
  }
})

export const name = style({
  flex: '1 1 auto',
  overflow: 'hidden',
  selectors: {
    [`${fileTableStyles.mobileRows} &`]: {
      width: '100%',
      lineHeight: vars.typography.bodyLarge.lineHeight
    }
  }
})

export const mobileMeta = style({
  display: 'none',
  selectors: {
    [`${fileTableStyles.mobileRows} &`]: {
      display: 'block',
      maxWidth: '100%',
      overflow: 'hidden',
      color: vars.color.text.secondary,
      fontSize: vars.typography.bodySmall.size,
      lineHeight: vars.typography.bodySmall.lineHeight,
      textOverflow: 'ellipsis',
      whiteSpace: 'nowrap'
    },
    [`${fileTableStyles.mobileRows} ${selected} &`]: {
      color: 'inherit',
      opacity: '.78'
    }
  }
})

// Shown on hover, focus and selection, and always on touch rows where there is no hover.
export const moreBtn = style({
  opacity: 0,
  transition: `opacity ${vars.motion.short} ${vars.motion.easing}`,
  selectors: {
    [`${root}:hover &, ${selected} &, ${focused} &, &:focus-visible, ${fileTableStyles.mobileRows} &`]: { opacity: 1 }
  }
})

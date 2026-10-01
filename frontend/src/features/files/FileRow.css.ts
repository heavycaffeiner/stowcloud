import { style } from '@vanilla-extract/css'
import * as fileTableStyles from './FileTable.css'
import { vars } from '../../ui/theme.css'

export const root = style({
  display: 'flex',
  alignItems: 'center',
  height: vars.layout.rowHeight,
  paddingInline: vars.layout.contentPad,
  cursor: 'pointer',
  borderBottom: 'none',
  color: vars.content.primary,
  fontSize: vars.typescale.bodyLarge.size,
  fontWeight: vars.typescale.bodyLarge.weight,
  lineHeight: vars.typescale.bodyLarge.lineHeight,
  userSelect: 'none',
  WebkitUserSelect: 'none',
  transition: 'background-color 120ms ease, color 120ms ease',
  selectors: {
    '&:hover': {
      background: `color-mix(in srgb, ${vars.content.primary} 5%, transparent)`
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
  background: vars.state.selection,
  color: vars.state.selectionContent,
  selectors: {
    '&:hover': {
      background: `color-mix(in srgb, ${vars.state.selection} 85%, ${vars.content.primary})`
    }
  }
})

export const focused = style({
  outline: 'none',
  selectors: {
    [`${fileTableStyles.root}:focus-visible &`]: {
      outline: `${vars.focusRing.width} solid ${vars.state.focus}`,
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
  minHeight: vars.control.minDesktop,
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
  color: vars.content.secondary,
  fontSize: vars.typescale.bodyMedium.size,
  lineHeight: vars.typescale.bodyMedium.lineHeight,
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
  color: vars.content.secondary,
  fontSize: vars.typescale.bodyMedium.size,
  lineHeight: vars.typescale.bodyMedium.lineHeight,
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
      lineHeight: vars.typescale.bodyLarge.lineHeight
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
      color: vars.content.secondary,
      fontSize: vars.typescale.bodySmall.size,
      lineHeight: vars.typescale.bodySmall.lineHeight,
      textOverflow: 'ellipsis',
      whiteSpace: 'nowrap'
    },
    [`${fileTableStyles.mobileRows} ${selected} &`]: {
      color: 'inherit',
      opacity: '.78'
    }
  }
})

export const moreBtn = style({
  width: vars.control.minDesktop,
  height: vars.control.minDesktop,
  borderRadius: '50%',
  border: 'none',
  background: 'transparent',
  color: vars.content.secondary,
  display: 'inline-flex',
  alignItems: 'center',
  justifyContent: 'center',
  cursor: 'pointer',
  opacity: '0',
  padding: '0',
  transition: 'opacity 120ms ease, background-color 120ms ease, color 120ms ease',
  selectors: {
    [`${root}:hover &`]: {
      opacity: '1'
    },
    [`${selected} &`]: {
      opacity: '1'
    },
    [`${focused} &`]: {
      opacity: '1'
    },
    '&:focus-visible': {
      outline: `${vars.focusRing.width} solid ${vars.state.focus}`,
      outlineOffset: vars.focusRing.offset
    },
    '&:hover': {
      background: `color-mix(in srgb, ${vars.content.primary} 10%, transparent)`,
      color: vars.content.primary
    },
    [`${fileTableStyles.mobileRows} &`]: {
      width: '44px',
      height: '44px',
      opacity: '1'
    }
  }
})

export const customCheckbox = style({
  width: '18px',
  height: '18px',
  borderRadius: '4px',
  border: `1.5px solid ${vars.outline.default}`,
  display: 'inline-flex',
  alignItems: 'center',
  justifyContent: 'center',
  background: 'transparent',
  color: `rgb(${vars.color.onPrimary})`,
  boxSizing: 'border-box',
  cursor: 'pointer',
  transition: 'background-color 120ms ease, border-color 120ms ease'
})

export const customCheckboxChecked = style({
  background: `rgb(${vars.color.primary})`,
  borderColor: `rgb(${vars.color.primary})`
})

export const customCheckboxIndeterminate = style({
  background: `rgb(${vars.color.primary})`,
  borderColor: `rgb(${vars.color.primary})`
})

export const customCheckboxBar = style({
  width: '10px',
  height: '2px',
  borderRadius: '1px',
  background: `rgb(${vars.color.onPrimary})`
})

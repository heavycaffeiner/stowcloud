import { style } from '@vanilla-extract/css'
import * as searchSheetStyles from './SearchSheet.css'
import { vars } from '@/shared/theme'

export const categoryPill = style({
  selectors: {
    [`${searchSheetStyles.root} &:focus-visible`]: {
      outline: `${vars.focusRing.width} solid ${vars.color.focus}`,
      outlineOffset: vars.focusRing.offset
    },
    '&:hover': {
      background: `color-mix(in srgb, ${vars.color.text.primary} 10%, transparent)`,
      color: vars.color.text.primary
    },
    '&:focus-visible': {
      outline: `${vars.focusRing.width} solid ${vars.color.focus}`,
      outlineOffset: vars.focusRing.insetOffset
    }
  },
  display: 'inline-flex',
  alignItems: 'center',
  gap: '6px',
  minHeight: vars.density.control,
  padding: '0 12px',
  borderRadius: '20px',
  background: `color-mix(in srgb, ${vars.color.text.primary} 5%, transparent)`,
  border: `1px solid ${vars.color.border.subtle}`,
  color: vars.color.text.secondary,
  fontFamily: 'inherit',
  fontSize: vars.typography.label.size,
  lineHeight: vars.typography.label.lineHeight,
  fontWeight: '500',
  cursor: 'pointer',
  whiteSpace: 'nowrap',
  flex: 'none',
  transition: 'background-color 140ms ease, border-color 140ms ease, color 140ms ease',
  '@media': {
    '(max-width: 599.98px)': {
      minHeight: '44px',
      scrollSnapAlign: 'start'
    }
  }
})

export const root = style({
  display: 'flex',
  flexDirection: 'column',
  gap: '12px',
  width: '100%',
  color: vars.color.text.primary
})

export const queryBar = style({
  display: 'flex',
  alignItems: 'center',
  gap: '10px',
  height: '48px',
  padding: '0 12px 0 14px',
  background: vars.color.surface.raised,
  border: `1px solid ${vars.color.border.subtle}`,
  borderRadius: '12px',
  boxSizing: 'border-box',
  transition: 'border-color 150ms ease, box-shadow 150ms ease',
  selectors: {
    '&:focus-within': {
      borderColor: vars.color.accent.solid,
      boxShadow: `0 0 0 2px color-mix(in srgb, ${vars.color.accent.solid} 20%, transparent)`
    }
  },
  '@media': {
    '(max-width: 599.98px)': {
      height: '56px',
      paddingBlock: '5px'
    }
  }
})

export const queryIcon = style({
  display: 'inline-flex',
  alignItems: 'center',
  color: vars.color.text.secondary,
  flex: 'none'
})

export const input = style({
  flex: '1',
  minWidth: '0',
  height: '100%',
  border: 'none',
  background: 'transparent',
  color: vars.color.text.primary,
  fontFamily: 'inherit',
  fontSize: vars.typography.bodyLarge.size,
  lineHeight: vars.typography.bodyLarge.lineHeight,
  outline: 'none',
  padding: '0',
  selectors: {
    '&::placeholder': {
      color: vars.color.text.secondary
    }
  }
})

export const clearBtn = style({
  width: vars.density.control,
  height: vars.density.control,
  display: 'inline-flex',
  alignItems: 'center',
  justifyContent: 'center',
  borderRadius: '50%',
  border: 'none',
  background: 'transparent',
  color: vars.color.text.secondary,
  cursor: 'pointer',
  padding: '0',
  transition: 'background-color 120ms ease, color 120ms ease',
  selectors: {
    '&:hover': {
      background: `color-mix(in srgb, ${vars.color.text.primary} 10%, transparent)`,
      color: vars.color.text.primary
    },
    '&:focus-visible': {
      outline: `${vars.focusRing.width} solid ${vars.color.focus}`,
      outlineOffset: vars.focusRing.offset
    }
  }
})

export const submitBtn = style({
  minHeight: vars.density.control,
  padding: '0 10px',
  borderRadius: vars.radius.sm,
  border: 'none',
  background: 'transparent',
  color: vars.color.accent.solid,
  fontFamily: 'inherit',
  fontSize: vars.typography.label.size,
  lineHeight: vars.typography.label.lineHeight,
  fontWeight: '500',
  cursor: 'pointer',
  display: 'inline-flex',
  alignItems: 'center',
  justifyContent: 'center',
  transition: 'background-color 120ms ease, color 120ms ease',
  selectors: {
    '&:hover': {
      background: `color-mix(in srgb, ${vars.color.accent.solid} 10%, transparent)`,
      color: vars.color.accent.solid
    },
    '&:active': {
      background: `color-mix(in srgb, ${vars.color.accent.solid} 10%, transparent)`,
      color: vars.color.accent.solid
    },
    '&:focus-visible': {
      outline: `${vars.focusRing.width} solid ${vars.color.focus}`,
      outlineOffset: vars.focusRing.offset
    }
  }
})

export const filterBar = style({
  display: 'flex',
  alignItems: 'center',
  justifyContent: 'space-between',
  gap: '8px',
  padding: '2px 0 4px',
  overflow: 'visible',
  scrollbarWidth: 'none',
  '@media': {
    '(max-width: 599.98px)': {
      alignItems: 'stretch',
      flexDirection: 'column',
      gap: '8px',
      overflow: 'visible'
    }
  }
})

export const categories = style({
  display: 'flex',
  alignItems: 'center',
  gap: '6px',
  flex: '1 1 auto',
  overflowX: 'auto',
  scrollbarWidth: 'none',
  '@media': {
    '(max-width: 599.98px)': {
      width: '100%',
      flex: 'none',
      paddingBottom: '2px',
      overscrollBehaviorInline: 'contain',
      scrollSnapType: 'x proximity'
    }
  }
})

export const categoryPillActive = style({
  background: vars.color.selection.bg,
  borderColor: `color-mix(in srgb, ${vars.color.selection.fg} 35%, transparent)`,
  color: vars.color.selection.fg,
  fontWeight: '600',
  selectors: {
    '&:hover': {
      background: `color-mix(in srgb, ${vars.color.selection.bg} 88%, ${vars.color.text.primary})`,
      color: vars.color.selection.fg
    }
  }
})

export const scopePill = style({
  display: 'inline-flex',
  alignItems: 'center',
  gap: '5px',
  height: '30px',
  padding: '0 10px',
  borderRadius: '15px',
  background: `color-mix(in srgb, ${vars.color.selection.bg} 55%, transparent)`,
  border: `1px solid color-mix(in srgb, ${vars.color.selection.fg} 25%, transparent)`,
  color: vars.color.selection.fg,
  fontSize: vars.typography.labelSmall.size,
  lineHeight: vars.typography.labelSmall.lineHeight,
  fontWeight: '500',
  whiteSpace: 'nowrap',
  flex: 'none',
  '@media': {
    '(max-width: 599.98px)': {
      alignSelf: 'flex-start',
      minHeight: '32px'
    }
  }
})

export const sortWrap = style({
  position: 'relative',
  flex: 'none',
  '@media': {
    '(max-width: 599.98px)': {
      width: '100%'
    }
  }
})

export const sortBtn = style({
  display: 'inline-flex',
  alignItems: 'center',
  gap: '6px',
  minHeight: vars.density.control,
  padding: '0 10px',
  borderRadius: '20px',
  background: 'transparent',
  border: `1px solid ${vars.color.border.subtle}`,
  color: vars.color.text.secondary,
  fontFamily: 'inherit',
  fontSize: vars.typography.labelSmall.size,
  lineHeight: vars.typography.labelSmall.lineHeight,
  fontWeight: '500',
  cursor: 'pointer',
  whiteSpace: 'nowrap',
  transition: 'background-color 140ms ease, color 140ms ease',
  selectors: {
    '&:focus-visible': {
      outline: `${vars.focusRing.width} solid ${vars.color.focus}`,
      outlineOffset: vars.focusRing.offset
    },
    '&:hover': {
      background: `color-mix(in srgb, ${vars.color.text.primary} 8%, transparent)`,
      color: vars.color.text.primary
    }
  },
  '@media': {
    '(max-width: 599.98px)': {
      width: '100%',
      minHeight: '44px',
      justifyContent: 'center'
    }
  }
})

export const menuItem = style({
  selectors: {
    '&:focus-visible': {
      outline: `${vars.focusRing.width} solid ${vars.color.focus}`,
      outlineOffset: vars.focusRing.offset
    },
    '&:hover': {
      background: `color-mix(in srgb, ${vars.color.text.primary} 8%, transparent)`,
      color: vars.color.text.primary
    }
  },
  display: 'flex',
  alignItems: 'center',
  minHeight: vars.density.control,
  padding: '8px 12px',
  border: 'none',
  background: 'transparent',
  color: vars.color.text.primary,
  fontFamily: 'inherit',
  fontSize: vars.typography.label.size,
  lineHeight: vars.typography.label.lineHeight,
  textAlign: 'left',
  borderRadius: '6px',
  cursor: 'pointer',
  transition: 'background-color 120ms ease'
})

export const stopBtn = style({
  selectors: {
    '&:focus-visible': {
      outline: `${vars.focusRing.width} solid ${vars.color.focus}`,
      outlineOffset: vars.focusRing.offset
    },
    '&:hover': {
      background: `color-mix(in srgb, ${vars.color.danger.solid} 12%, transparent)`
    }
  },
  minHeight: vars.density.control,
  display: 'inline-flex',
  alignItems: 'center',
  background: 'transparent',
  border: 'none',
  color: vars.color.danger.solid,
  fontSize: vars.typography.label.size,
  lineHeight: vars.typography.label.lineHeight,
  cursor: 'pointer',
  padding: '0 8px',
  borderRadius: '8px',
  '@media': {
    '(max-width: 599.98px)': {
      minHeight: '44px',
      paddingInline: '14px'
    }
  }
})

export const menu = style({
  position: 'absolute',
  top: 'calc(100% + 4px)',
  right: '0',
  zIndex: '50',
  minWidth: '140px',
  padding: '4px',
  background: vars.color.surface.overlay,
  border: `1px solid ${vars.color.border.subtle}`,
  borderRadius: '10px',
  boxShadow: '0 8px 24px rgba(0, 0, 0, 0.28)',
  display: 'flex',
  flexDirection: 'column',
  gap: '2px'
})

export const statusBar = style({
  display: 'flex',
  alignItems: 'center',
  justifyContent: 'space-between',
  minHeight: '24px',
  padding: '0 2px',
  fontSize: vars.typography.bodySmall.size,
  lineHeight: vars.typography.bodySmall.lineHeight,
  color: vars.color.text.secondary,
  '@media': {
    '(max-width: 599.98px)': {
      alignItems: 'stretch',
      flexDirection: 'column',
      gap: '8px'
    }
  }
})

export const statusInfo = style({
  display: 'inline-flex',
  alignItems: 'center',
  gap: '6px'
})

export const progress = style({
  display: 'inline-flex',
  alignItems: 'center',
  width: '14px',
  height: '14px'
})

export const statusActions = style({
  display: 'flex',
  alignItems: 'center',
  gap: '8px',
  '@media': {
    '(max-width: 599.98px)': {
      justifyContent: 'flex-end'
    }
  }
})

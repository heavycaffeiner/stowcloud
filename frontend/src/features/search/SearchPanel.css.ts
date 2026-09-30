import { style } from '@vanilla-extract/css'
import * as searchSheetStyles from './SearchSheet.css'
import { vars } from '../../ui/theme.css'

export const categoryPill = style({
  selectors: {
    [`${searchSheetStyles.root} &:focus-visible`]: {
      outline: `${vars.focusRing.width} solid ${vars.state.focus}`,
      outlineOffset: vars.focusRing.offset
    },
    '&:hover': {
      background: `color-mix(in srgb, ${vars.content.primary} 10%, transparent)`,
      color: vars.content.primary
    },
    '&:focus-visible': {
      outline: `${vars.focusRing.width} solid ${vars.state.focus}`,
      outlineOffset: vars.focusRing.insetOffset
    }
  },
  display: 'inline-flex',
  alignItems: 'center',
  gap: '6px',
  minHeight: vars.control.min,
  padding: '0 12px',
  borderRadius: '20px',
  background: `color-mix(in srgb, ${vars.content.primary} 5%, transparent)`,
  border: `1px solid ${vars.outline.variant}`,
  color: vars.content.secondary,
  fontFamily: 'inherit',
  fontSize: vars.typescale.labelLarge.size,
  lineHeight: vars.typescale.labelLarge.lineHeight,
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
  color: vars.content.primary
})

export const queryBar = style({
  display: 'flex',
  alignItems: 'center',
  gap: '10px',
  height: '48px',
  padding: '0 12px 0 14px',
  background: vars.surface.raised,
  border: `1px solid ${vars.outline.variant}`,
  borderRadius: '12px',
  boxSizing: 'border-box',
  transition: 'border-color 150ms ease, box-shadow 150ms ease',
  selectors: {
    '&:focus-within': {
      borderColor: `rgb(${vars.color.primary})`,
      boxShadow: `0 0 0 2px rgba(${vars.color.primary}, 0.2)`
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
  color: vars.content.secondary,
  flex: 'none'
})

export const input = style({
  flex: '1',
  minWidth: '0',
  height: '100%',
  border: 'none',
  background: 'transparent',
  color: vars.content.primary,
  fontFamily: 'inherit',
  fontSize: vars.typescale.bodyLarge.size,
  lineHeight: vars.typescale.bodyLarge.lineHeight,
  outline: 'none',
  padding: '0',
  selectors: {
    '&::placeholder': {
      color: vars.content.secondary
    }
  }
})

export const clearBtn = style({
  width: vars.control.min,
  height: vars.control.min,
  display: 'inline-flex',
  alignItems: 'center',
  justifyContent: 'center',
  borderRadius: '50%',
  border: 'none',
  background: 'transparent',
  color: vars.content.secondary,
  cursor: 'pointer',
  padding: '0',
  transition: 'background-color 120ms ease, color 120ms ease',
  selectors: {
    '&:hover': {
      background: `color-mix(in srgb, ${vars.content.primary} 10%, transparent)`,
      color: vars.content.primary
    },
    '&:focus-visible': {
      outline: `${vars.focusRing.width} solid ${vars.state.focus}`,
      outlineOffset: vars.focusRing.offset
    }
  }
})

export const submitBtn = style({
  minHeight: vars.control.min,
  padding: '0 10px',
  borderRadius: vars.radius.control,
  border: 'none',
  background: 'transparent',
  color: `rgb(${vars.color.primary})`,
  fontFamily: 'inherit',
  fontSize: vars.typescale.labelLarge.size,
  lineHeight: vars.typescale.labelLarge.lineHeight,
  fontWeight: '500',
  cursor: 'pointer',
  display: 'inline-flex',
  alignItems: 'center',
  justifyContent: 'center',
  transition: 'background-color 120ms ease, color 120ms ease',
  selectors: {
    '&:hover': {
      background: `color-mix(in srgb, rgb(${vars.color.primary}) 10%, transparent)`,
      color: `rgb(${vars.color.primary})`
    },
    '&:active': {
      background: `color-mix(in srgb, rgb(${vars.color.primary}) 10%, transparent)`,
      color: `rgb(${vars.color.primary})`
    },
    '&:focus-visible': {
      outline: `${vars.focusRing.width} solid ${vars.state.focus}`,
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
  background: vars.state.selection,
  borderColor: `color-mix(in srgb, ${vars.state.selectionContent} 35%, transparent)`,
  color: vars.state.selectionContent,
  fontWeight: '600',
  selectors: {
    '&:hover': {
      background: `color-mix(in srgb, ${vars.state.selection} 88%, ${vars.content.primary})`,
      color: vars.state.selectionContent
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
  background: `color-mix(in srgb, ${vars.state.selection} 55%, transparent)`,
  border: `1px solid color-mix(in srgb, ${vars.state.selectionContent} 25%, transparent)`,
  color: vars.state.selectionContent,
  fontSize: vars.typescale.labelMedium.size,
  lineHeight: vars.typescale.labelMedium.lineHeight,
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
  minHeight: vars.control.min,
  padding: '0 10px',
  borderRadius: '20px',
  background: 'transparent',
  border: `1px solid ${vars.outline.variant}`,
  color: vars.content.secondary,
  fontFamily: 'inherit',
  fontSize: vars.typescale.labelMedium.size,
  lineHeight: vars.typescale.labelMedium.lineHeight,
  fontWeight: '500',
  cursor: 'pointer',
  whiteSpace: 'nowrap',
  transition: 'background-color 140ms ease, color 140ms ease',
  selectors: {
    '&:focus-visible': {
      outline: `${vars.focusRing.width} solid ${vars.state.focus}`,
      outlineOffset: vars.focusRing.offset
    },
    '&:hover': {
      background: `color-mix(in srgb, ${vars.content.primary} 8%, transparent)`,
      color: vars.content.primary
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
      outline: `${vars.focusRing.width} solid ${vars.state.focus}`,
      outlineOffset: vars.focusRing.offset
    },
    '&:hover': {
      background: `color-mix(in srgb, ${vars.content.primary} 8%, transparent)`,
      color: vars.content.primary
    }
  },
  display: 'flex',
  alignItems: 'center',
  minHeight: vars.control.min,
  padding: '8px 12px',
  border: 'none',
  background: 'transparent',
  color: vars.content.primary,
  fontFamily: 'inherit',
  fontSize: vars.typescale.labelLarge.size,
  lineHeight: vars.typescale.labelLarge.lineHeight,
  textAlign: 'left',
  borderRadius: '6px',
  cursor: 'pointer',
  transition: 'background-color 120ms ease'
})

export const stopBtn = style({
  selectors: {
    '&:focus-visible': {
      outline: `${vars.focusRing.width} solid ${vars.state.focus}`,
      outlineOffset: vars.focusRing.offset
    },
    '&:hover': {
      background: `color-mix(in srgb, rgb(${vars.color.error}) 12%, transparent)`
    }
  },
  minHeight: vars.control.min,
  display: 'inline-flex',
  alignItems: 'center',
  background: 'transparent',
  border: 'none',
  color: `rgb(${vars.color.error})`,
  fontSize: vars.typescale.labelLarge.size,
  lineHeight: vars.typescale.labelLarge.lineHeight,
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
  background: vars.surface.overlay,
  border: `1px solid ${vars.outline.variant}`,
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
  fontSize: vars.typescale.bodySmall.size,
  lineHeight: vars.typescale.bodySmall.lineHeight,
  color: vars.content.secondary,
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

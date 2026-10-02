import { createContainer, createVar, fallbackVar, style } from '@vanilla-extract/css'
import { vars } from '../../../ui/theme.css'

export const selectionBarSpace = createVar()

export const modifiedColumnWidth = createVar()

export const container = createContainer()

export const root = style({
  position: 'relative',
  flex: '1',
  minWidth: '0',
  alignSelf: 'flex-start',
  contain: 'content',
  background: vars.surface.page,
  color: vars.content.primary,
  containerType: 'inline-size',
  containerName: container,
  paddingBottom: `calc(24px + ${fallbackVar(selectionBarSpace, '0px')})`,
  vars: {
    [modifiedColumnWidth]: '11rem'
  },
  selectors: {
    '&:focus-visible': {
      outline: `${vars.focusRing.width} solid ${vars.state.focus}`,
      outlineOffset: vars.focusRing.insetOffset
    }
  }
})

export const contained = style({
  alignSelf: 'stretch',
  minHeight: '0',
  overflow: 'auto',
  overscrollBehaviorY: 'contain'
})

export const reserveSelection = style({
  vars: {
    [selectionBarSpace]: '80px'
  }
})

export const mobileRows = style({})

export const header = style({
  position: 'sticky',
  zIndex: '2',
  top: '0',
  display: 'flex',
  alignItems: 'center',
  height: '40px',
  paddingInline: vars.layout.contentPad,
  borderBottom: 'none',
  background: `color-mix(in srgb, ${vars.surface.container} 70%, ${vars.surface.page})`,
  color: vars.content.secondary,
  fontSize: vars.typescale.labelLarge.size,
  fontWeight: vars.typescale.labelLarge.weight,
  lineHeight: vars.typescale.labelLarge.lineHeight,
  letterSpacing: vars.typescale.labelLarge.tracking,
  '@container': {
    [`${container} (max-width: 599.98px)`]: {
      height: '40px',
      paddingInline: '16px'
    }
  },
  selectors: {
    [`${mobileRows} &`]: {
      paddingInline: '12px'
    }
  }
})

export const headerCell = style({
  display: 'flex',
  alignItems: 'center',
  minWidth: '0',
  gap: '4px',
  overflow: 'hidden'
})

export const headerCellSelect = style({
  flex: '0 0 40px',
  justifyContent: 'center',
  border: '0',
  background: 'transparent',
  padding: '0',
  color: 'inherit',
  cursor: 'pointer',
  selectors: {
    [`${mobileRows} &`]: {
      flexBasis: '44px'
    }
  }
})

export const headerCellName = style({
  flex: '1 1 auto'
})

export const headerCellSize = style({
  flex: '0 0 112px',
  justifyContent: 'flex-end',
  '@container': {
    [`${container} (max-width: 599.98px)`]: {
      flexBasis: '88px'
    }
  },
  selectors: {
    [`${mobileRows} &`]: {
      display: 'none'
    }
  }
})

export const headerCellMtime = style({
  flex: `0 0 ${modifiedColumnWidth}`,
  justifyContent: 'flex-end',
  whiteSpace: 'nowrap',
  '@container': {
    [`${container} (max-width: 599.98px)`]: {
      display: 'none'
    }
  },
  selectors: {
    [`${mobileRows} &`]: {
      display: 'none'
    }
  }
})

export const headerCellActions = style({
  flex: '0 0 40px',
  selectors: {
    [`${mobileRows} &`]: {
      flexBasis: '44px'
    }
  }
})

export const headerButton = style({
  display: 'inline-flex',
  alignItems: 'center',
  gap: '4px',
  minWidth: '0',
  minHeight: vars.control.minDesktop,
  padding: '6px 4px',
  border: '0',
  borderRadius: vars.shape.cornerExtraSmall,
  background: 'transparent',
  color: 'inherit',
  font: 'inherit',
  cursor: 'pointer',
  transition: 'background-color 120ms ease, color 120ms ease, transform 100ms ease',
  selectors: {
    '&:active': {
      transform: 'scale(0.96)'
    },
    '&:hover': {
      background: 'color-mix(in srgb, currentColor 10%, transparent)',
      color: vars.content.primary
    },
    '&:focus-visible': {
      outline: `${vars.focusRing.width} solid ${vars.state.focus}`,
      outlineOffset: vars.focusRing.offset
    }
  }
})

export const headerButtonActive = style({
  color: `rgb(${vars.color.primary})`,
  fontWeight: '600'
})

export const headerSort = style({
  display: 'inline-flex',
  flex: 'none',
  fontWeight: '700'
})

export const spacer = style({
  position: 'relative'
})

export const window = style({
  willChange: 'transform'
})

export const empty = style({
  display: 'flex',
  alignItems: 'center',
  justifyContent: 'center',
  minHeight: '160px',
  padding: '64px 24px',
  color: vars.content.secondary,
  fontSize: vars.typescale.bodyLarge.size
})

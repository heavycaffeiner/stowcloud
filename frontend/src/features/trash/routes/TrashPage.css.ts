import { style } from '@vanilla-extract/css'
import { vars } from '@/shared/theme'

export const noticeClose = style({
  selectors: {
    '&:focus-visible': {
      outline: `${vars.focusRing.width} solid ${vars.color.focus}`,
      outlineOffset: vars.focusRing.offset
    }
  },
  display: 'inline-flex',
  alignItems: 'center',
  justifyContent: 'center',
  minWidth: '40px',
  minHeight: '40px',
  padding: '4px 8px',
  border: '0',
  borderRadius: '20px',
  background: 'transparent',
  color: 'inherit',
  cursor: 'pointer',
  '@media': {
    '(max-width: 599.98px)': {
      minHeight: '44px'
    }
  }
})

export const toolbar = style({
  display: 'flex',
  alignItems: 'center',
  justifyContent: 'space-between',
  flexWrap: 'wrap',
  gap: '8px',
  padding: '8px 16px',
  marginBottom: '8px',
  borderRadius: '12px',
  background: vars.color.surface.container,
  '@media': {
    '(max-width: 599.98px)': {
      paddingInline: '12px'
    }
  }
})

export const selectAll = style({
  display: 'inline-flex',
  alignItems: 'center',
  gap: '8px',
  minHeight: '40px'
})

export const toolbarActions = style({
  display: 'flex',
  flex: 'none',
  alignItems: 'center',
  gap: '4px',
  '@media': {
    '(max-width: 599.98px)': {
      width: '100%',
      justifyContent: 'flex-end'
    }
  }
})

export const row = style({
  display: 'flex',
  alignItems: 'center',
  gap: '12px',
  padding: '8px 16px',
  minWidth: '0',
  borderBottom: `1px solid ${vars.color.border.subtle}`,
  '@media': {
    '(max-width: 599.98px)': {
      gap: '8px',
      paddingInline: '8px'
    }
  }
})

export const notice = style({
  display: 'grid',
  gridTemplateColumns: 'minmax(0, 1fr) auto',
  alignItems: 'center',
  gap: '8px',
  margin: '0 0 8px',
  padding: '8px 12px 8px 16px',
  overflowWrap: 'anywhere',
  borderRadius: '8px',
  background: vars.color.highlight.soft,
  color: vars.color.highlight.onSoft
})

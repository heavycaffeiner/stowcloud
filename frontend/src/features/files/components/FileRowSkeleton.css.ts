import { keyframes, style } from '@vanilla-extract/css'
import * as fileTableStyles from './FileTable.css'
import { vars } from '@/shared/theme'

const pulse = keyframes({
  '0%, 100%': {
    opacity: '.5'
  },
  '50%': {
    opacity: '1'
  }
})

export const root = style({
  display: 'flex',
  alignItems: 'center',
  height: vars.density.row,
  paddingInline: '16px',
  borderBottom: 'none',
  '@container': {
    [`${fileTableStyles.container} (max-width: 599.98px)`]: {
      paddingInline: '12px'
    }
  },
  selectors: {
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
  flex: '0 0 36px',
  selectors: {
    [`${fileTableStyles.mobileRows} &`]: {
      flexBasis: '44px'
    }
  }
})

export const cellName = style({
  flex: '1 1 auto',
  minWidth: '0'
})

export const cellSize = style({
  flex: '0 0 112px',
  justifyContent: 'flex-end',
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

export const bar = style({
  display: 'block',
  height: '16px',
  borderRadius: vars.radius.xs,
  background: vars.color.surface.fill,
  animation: `${pulse} 1.2s ease-in-out infinite`,
  '@media': {
    '(prefers-reduced-motion: reduce)': {
      transition: 'none',
      animation: 'none'
    }
  }
})

export const barIcon = style({
  flex: '0 0 20px',
  height: '20px',
  borderRadius: vars.radius.sm
})

export const barName = style({
  width: '60%'
})

export const barSize = style({
  width: '40px'
})

export const barMtime = style({
  width: '88px'
})

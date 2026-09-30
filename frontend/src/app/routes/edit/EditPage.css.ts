import { style } from '@vanilla-extract/css'
import { vars } from '../../../ui/theme.css'

export const root = style({
  display: 'flex',
  flexDirection: 'column',
  height: '100%',
  minHeight: '0',
  background: `rgb(${vars.color.surfaceContainerLow})`,
  color: `rgb(${vars.color.onSurface})`
})

export const toolbar = style({
  zIndex: '2',
  display: 'flex',
  alignItems: 'center',
  gap: '12px',
  minHeight: '76px',
  padding: '10px 20px',
  background: `rgb(${vars.color.surface})`,
  boxShadow: `0 1px 0 rgb(${vars.color.outlineVariant})`,
  '@media': {
    '(max-width: 600px)': {
      gap: '8px',
      minHeight: '64px',
      padding: '8px'
    }
  }
})

export const fileIcon = style({
  display: 'inline-flex',
  flex: 'none',
  alignItems: 'center',
  justifyContent: 'center',
  width: '40px',
  height: '40px',
  borderRadius: vars.shape.cornerMedium,
  background: `rgb(${vars.color.primaryContainer})`,
  color: `rgb(${vars.color.onPrimaryContainer})`,
  fontSize: '1.35rem',
  '@media': {
    '(max-width: 600px)': {
      display: 'none'
    }
  }
})

export const identity = style({
  display: 'flex',
  flex: '1',
  flexDirection: 'column',
  gap: '4px',
  minWidth: '0'
})

export const title = style({
  display: 'flex',
  alignItems: 'center',
  gap: '8px',
  minWidth: '0',
  '@media': {
    '(max-width: 600px)': {
      gap: '6px'
    }
  }
})

export const details = style({
  display: 'flex',
  alignItems: 'center',
  gap: '8px',
  minWidth: '0',
  color: `rgb(${vars.color.onSurfaceVariant})`,
  fontSize: vars.typescale.bodySmall.size,
  lineHeight: '1.25rem'
})

export const filename = style({
  overflow: 'hidden',
  color: `rgb(${vars.color.onSurface})`,
  fontSize: vars.typescale.titleMedium.size,
  fontWeight: '600',
  lineHeight: '1.35',
  textOverflow: 'ellipsis',
  whiteSpace: 'nowrap'
})

export const language = style({
  display: 'inline-flex',
  flex: 'none',
  alignItems: 'center',
  minHeight: '22px',
  paddingInline: '8px',
  borderRadius: vars.shape.cornerFull,
  fontSize: vars.typescale.labelSmall.size,
  fontWeight: '600',
  lineHeight: '1',
  background: `rgb(${vars.color.secondaryContainer})`,
  color: `rgb(${vars.color.onSecondaryContainer})`
})

export const badge = style({
  display: 'inline-flex',
  flex: 'none',
  alignItems: 'center',
  minHeight: '22px',
  paddingInline: '8px',
  borderRadius: vars.shape.cornerFull,
  fontSize: vars.typescale.labelSmall.size,
  fontWeight: '600',
  lineHeight: '1'
})

export const badgeDirty = style({
  background: `rgb(${vars.color.primaryContainer})`,
  color: `rgb(${vars.color.onPrimaryContainer})`,
  '@media': {
    '(max-width: 600px)': {
      width: '8px',
      minHeight: '8px',
      padding: '0',
      overflow: 'hidden',
      color: 'transparent',
      fontSize: '0'
    }
  }
})

export const badgeReadonly = style({
  background: `rgb(${vars.color.errorContainer})`,
  color: `rgb(${vars.color.onErrorContainer})`
})

export const meta = style({
  flex: 'none',
  '@media': {
    '(max-width: 600px)': {
      display: 'none'
    }
  }
})

export const actions = style({
  flex: 'none',
  display: 'flex',
  alignItems: 'center'
})

export const body = style({
  position: 'relative',
  display: 'flex',
  flex: '1',
  minHeight: '0',
  padding: '16px 20px 20px',
  '@media': {
    '(max-width: 600px)': {
      padding: '0'
    }
  }
})

export const loading = style({
  display: 'flex',
  flex: '1',
  alignItems: 'center',
  justifyContent: 'center'
})

export const locked = style({
  display: 'flex',
  flex: '1',
  alignItems: 'center',
  justifyContent: 'center',
  flexDirection: 'column',
  gap: '16px',
  padding: '24px',
  textAlign: 'center'
})

export const error = style({
  margin: '0',
  padding: '24px',
  color: `rgb(${vars.color.error})`
})

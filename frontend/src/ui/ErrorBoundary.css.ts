import { style } from '@vanilla-extract/css'

export const notice = style({
  display: 'flex',
  flexWrap: 'wrap',
  alignItems: 'center',
  justifyContent: 'space-between',
  gap: '8px 16px',
  padding: '12px 16px',
  borderRadius: 'var(--sc-radius-small)',
  border: '1px solid var(--sc-outline-variant)',
  background: 'var(--sc-raised-surface)',
  color: 'rgb(var(--mdui-color-on-surface))'
})

export const message = style({
  margin: '0',
  overflowWrap: 'anywhere'
})

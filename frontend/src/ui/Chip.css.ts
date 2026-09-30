import { style } from '@vanilla-extract/css'

export const root = style({
  maxInlineSize: '100%',
  minInlineSize: '0',
  selectors: {
    '&::part(label)': {
      minInlineSize: '0',
      overflowWrap: 'anywhere'
    }
  }
})

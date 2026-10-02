import { globalStyle } from '@vanilla-extract/css'
import { vars } from './contract.css'
import { media } from './media'
import { typography } from './typography'

globalStyle(':root', {
  colorScheme: 'light dark',
  fontFamily: vars.font.family
})

globalStyle(':root[data-mantine-color-scheme="light"]', { colorScheme: 'light' })
globalStyle(':root[data-mantine-color-scheme="dark"]', { colorScheme: 'dark' })

globalStyle('*', { boxSizing: 'border-box' })

globalStyle('html, body, #root', { minHeight: '100%', margin: '0' })

globalStyle('html', { height: '100%' })

globalStyle('body', {
  minWidth: '320px',
  background: vars.color.surface.page,
  color: vars.color.text.primary,
  fontFamily: 'inherit',
  fontFeatureSettings: "'tnum' 1",
  ...typography('bodyLarge')
})

globalStyle(':where(button, input, select, textarea)', { font: 'inherit' })

globalStyle('svg', { display: 'inline-block', verticalAlign: 'middle', flexShrink: '0' })

globalStyle('h1, h2, h3, h4, h5, h6', { margin: '0', wordBreak: 'keep-all' })
globalStyle('h1', typography('heading'))
globalStyle('h2', typography('titleLarge'))
globalStyle('h3', typography('title'))
globalStyle('h4, h5, h6', typography('titleSmall'))

globalStyle('*, *::before, *::after', {
  '@media': {
    [media.reducedMotion]: {
      scrollBehavior: 'auto !important' as 'auto',
      animationDuration: '.01ms !important',
      animationIterationCount: '1 !important',
      transitionDuration: '.01ms !important'
    }
  }
})

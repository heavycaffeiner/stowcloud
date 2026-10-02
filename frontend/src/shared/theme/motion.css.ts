import { keyframes } from '@vanilla-extract/css'
import { vars } from './contract.css'

export const fadeInUp = keyframes({
  from: { opacity: 0, transform: `translateY(${vars.space.sm})` },
  to: { opacity: 1, transform: 'translateY(0)' }
})

export const scaleUp = keyframes({
  from: { opacity: 0, transform: 'scale(0.96)' },
  to: { opacity: 1, transform: 'scale(1)' }
})

export const fadeIn = keyframes({
  from: { opacity: 0 },
  to: { opacity: 1 }
})

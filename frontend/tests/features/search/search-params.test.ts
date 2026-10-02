import { describe, expect, it } from 'vitest'
import { validateSearchParams } from '../../../src/features/search/search-params'

describe('validateSearchParams', () => {
  it('opens search over the root when the scope is empty', () => {
    expect(validateSearchParams({ search: '' })).toEqual({ search: '' })
  })

  it('keeps search closed without a scope', () => {
    expect(validateSearchParams({ q: 'report' })).toEqual({ q: 'report' })
    expect(validateSearchParams({ search: 3 })).toEqual({})
  })

  it('drops an empty query', () => {
    expect(validateSearchParams({ search: '/docs', q: '' })).toEqual({ search: '/docs' })
  })
})

import { expect, it } from 'vitest'
import { smoke } from '../src/index.js'

it('consumes the released package through the public API', async () => {
  expect(new TextDecoder().decode(await smoke())).toBe('independent public consumer')
})

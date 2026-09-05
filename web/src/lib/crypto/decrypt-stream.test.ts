import { describe, expect, it } from 'vitest'
import { ByteAccumulator } from './decrypt-stream'

function bytes(...values: number[]): Uint8Array {
  return new Uint8Array(values)
}

describe('ByteAccumulator', () => {
  it('reports zero length before anything is pushed', () => {
    expect(new ByteAccumulator().length).toBe(0)
  })

  it('takes exactly n bytes from a single pushed chunk, leaving the rest buffered', () => {
    const acc = new ByteAccumulator()
    acc.push(bytes(1, 2, 3, 4, 5))
    expect(acc.length).toBe(5)
    expect(Array.from(acc.take(2))).toEqual([1, 2])
    expect(acc.length).toBe(3)
    expect(Array.from(acc.take(3))).toEqual([3, 4, 5])
    expect(acc.length).toBe(0)
  })

  it('merges several small pushes to satisfy one take spanning all of them', () => {
    const acc = new ByteAccumulator()
    acc.push(bytes(1))
    acc.push(bytes(2, 3))
    acc.push(bytes(4, 5, 6))
    expect(acc.length).toBe(6)
    expect(Array.from(acc.take(6))).toEqual([1, 2, 3, 4, 5, 6])
  })

  it('ignores an empty push rather than recording a zero-length chunk', () => {
    const acc = new ByteAccumulator()
    acc.push(bytes())
    acc.push(bytes(1, 2))
    expect(acc.length).toBe(2)
    expect(Array.from(acc.take(2))).toEqual([1, 2])
  })

  it('throws rather than returning a short read when too little is buffered', () => {
    const acc = new ByteAccumulator()
    acc.push(bytes(1, 2))
    expect(() => acc.take(3)).toThrow(RangeError)
    // The failed take did not consume anything.
    expect(acc.length).toBe(2)
  })

  it('drain returns every remaining byte and empties the buffer', () => {
    const acc = new ByteAccumulator()
    acc.push(bytes(9, 8, 7))
    expect(Array.from(acc.drain())).toEqual([9, 8, 7])
    expect(acc.length).toBe(0)
    // Draining an empty buffer is a valid zero-byte result, not an error.
    expect(Array.from(acc.drain())).toEqual([])
  })

  it('take(0) is a valid no-op that returns an empty slice', () => {
    const acc = new ByteAccumulator()
    acc.push(bytes(1, 2, 3))
    expect(Array.from(acc.take(0))).toEqual([])
    expect(acc.length).toBe(3)
  })
})

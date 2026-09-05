// The destination guard behind
// `DestinationPickerDialog`. The server rejects an illegal move too, but only
// after the user has committed to it and waited for a job to fail; these rules
// are what let the picker grey the button out and say why instead.
import { describe, expect, it } from 'vitest'
import { destinationProblem, isWithin } from './path-utils'

describe('isWithin', () => {
  it('counts a path as within itself', () => {
    expect(isWithin('/home/Docs', '/home/Docs')).toBe(true)
  })

  it('matches on the separator, not the bare string prefix', () => {
    // The bug this exists to prevent: '/home/Doc' starts with '/home/Do' and
    // '/home/Documents' starts with '/home/Doc', neither of which is nesting.
    expect(isWithin('/home/Documents', '/home/Doc')).toBe(false)
    expect(isWithin('/home/Doc', '/home/Documents')).toBe(false)
    expect(isWithin('/home/Doc/a', '/home/Doc')).toBe(true)
  })

  it('treats the root as containing everything', () => {
    expect(isWithin('/home/a', '/')).toBe(true)
  })

  it('normalizes trailing slashes and doubled separators on both sides', () => {
    expect(isWithin('/home//a/', '/home/')).toBe(true)
  })
})

describe('destinationProblem', () => {
  it('accepts an unrelated folder', () => {
    expect(destinationProblem('/home/Photos', ['/home/Docs/a.txt'])).toBeNull()
  })

  it('rejects a folder being sent into itself', () => {
    expect(destinationProblem('/home/Docs', ['/home/Docs'])).toBe('into_itself')
  })

  it('rejects a folder being sent into its own subfolder', () => {
    expect(destinationProblem('/home/Docs/2026', ['/home/Docs'])).toBe('into_itself')
  })

  it("reports the source's own folder so a move can be blocked but a copy allowed", () => {
    expect(destinationProblem('/home/Docs', ['/home/Docs/a.txt'])).toBe('same_folder')
  })

  it('reports the fatal problem when a batch has both', () => {
    // 'into_itself' has to win: it is the one no mode can proceed with, and a
    // batch that hits both would otherwise be offered a copy that cannot run.
    expect(destinationProblem('/home/Docs', ['/home/Docs/a.txt', '/home/Docs'])).toBe('into_itself')
  })
})

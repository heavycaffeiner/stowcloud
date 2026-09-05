// The download ticket
// (`POST /api/v1/files/download`, mirrored by `format/download.ts`'s
// `downloadPath`) replaced every hand-built download URL in the app.
// `GET /api/v1/files/read` no longer honours `?download=1`; it renders the
// file instead of saving it, so a surviving call site would be a silent
// regression from "download" to "open in the tab". This walks the real
// source tree (not a fixture), so a future download button that reaches
// back for the old shortcut fails here rather than shipping.
import { readdirSync, readFileSync, statSync } from 'node:fs'
import { dirname, join, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'
import { describe, expect, it } from 'vitest'

const srcRoot = resolve(dirname(fileURLToPath(import.meta.url)), '../src')

function walk(dir: string, out: string[] = []): string[] {
  for (const name of readdirSync(dir)) {
    const p = join(dir, name)
    if (statSync(p).isDirectory()) {
      walk(p, out)
      continue
    }
    if (/\.(svelte|ts)$/.test(p)) out.push(p)
  }
  return out
}

describe('no client code composes a download URL from a path', () => {
  it('has no "download=1" anywhere in the app source', () => {
    const offenders = walk(srcRoot).filter((p) => readFileSync(p, 'utf8').includes('download=1'))
    expect(offenders).toEqual([])
  })
})

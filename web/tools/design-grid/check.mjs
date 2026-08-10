// tools/design-grid/check.mjs — the design gate's one entry point.
//
// Runs the three layers and owns the exit-code contract, so `npm run
// check:design` says the same thing whichever layer failed:
//
//   0  no violations; every waiver was used and none had expired
//   1  design violations survived waiver matching
//   2  waiver or policy configuration error, including a waiver that
//      matched nothing during the run
//   3  harness failure: the audit learned nothing, and its silence must not
//      read as a pass
//
// 2 and 3 are separated from 1 deliberately. A 1 says a value is wrong. A 2
// says the policy files are wrong. A 3 says nothing was measured.
//
// Each layer sweeps its own dead waivers, scoped to its own layer, so no
// cross-process reporting is needed: the static sweep runs in this process
// alongside stylelint, the component sweep is an assertion inside the vitest
// file, and the runtime sweep is inside the audit.

import { spawn } from 'node:child_process'
import path from 'node:path'
import process from 'node:process'
import { fileURLToPath, pathToFileURL } from 'node:url'
import stylelint from 'stylelint'
import { EXIT, runAudit } from './audit.mjs'
import { deadWaivers, sharedWaivers, WaiverConfigError } from './waivers.mjs'

const WEB_ROOT = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..', '..')

function heading(name) {
  console.log(`\n── ${name} ${'─'.repeat(Math.max(0, 60 - name.length))}`)
}

// ── layer 1: static ───────────────────────────────────────────────────────

async function runStatic() {
  heading('static (stylelint sc/four-px-grid)')

  const result = await stylelint.lint({
    cwd: WEB_ROOT,
    files: 'src/**/*.{css,svelte}',
    configFile: path.join(WEB_ROOT, '.stylelintrc.cjs'),
    formatter: 'unix'
  })

  // `report` since stylelint 16; `output` is the older name and is undefined
  // here, which is what made this throw on its first run.
  const text = (result.report ?? result.output ?? '').trim()
  if (text) console.log(text)

  const count = result.results.reduce((n, r) => n + r.warnings.length, 0)
  console.log(`design-grid static: ${result.results.length} files, ${count} violations`)

  const dead = deadWaivers(sharedWaivers(), ['static'])
  for (const w of dead) {
    console.log(`design-grid: waiver "${w.id}" matched nothing. Remove it or fix its selector.`)
  }
  if (dead.length > 0) return EXIT.CONFIG
  return count > 0 ? EXIT.VIOLATIONS : EXIT.OK
}

// ── layer 2: component ────────────────────────────────────────────────────

function runComponent() {
  heading('component (vitest, jsdom)')

  return new Promise((resolve) => {
    const child = spawn(
      process.execPath,
      [
        path.join(WEB_ROOT, 'node_modules/vitest/vitest.mjs'),
        'run',
        'tools/design-grid/component.test.ts'
      ],
      { cwd: WEB_ROOT, stdio: 'inherit' }
    )
    // vitest exits 1 for a failing assertion, which here means a violation or
    // a dead component waiver, and anything else means it could not run.
    child.on('exit', (code) => resolve(code === 0 ? EXIT.OK : code === 1 ? EXIT.VIOLATIONS : EXIT.HARNESS))
    child.on('error', () => resolve(EXIT.HARNESS))
  })
}

// ── the gate ──────────────────────────────────────────────────────────────

export async function checkDesign() {
  try {
    sharedWaivers()
  } catch (e) {
    if (e instanceof WaiverConfigError) {
      console.error(`design-grid: ${e.message}`)
      return EXIT.CONFIG
    }
    throw e
  }

  const codes = []
  codes.push(await runStatic())
  codes.push(await runComponent())

  heading('runtime (playwright, chromium)')
  codes.push(await runAudit({}))

  // Worst wins, and the ordering says why: a harness failure hides whatever a
  // violation count would have been, and a broken policy file hides both.
  const worst = [EXIT.HARNESS, EXIT.CONFIG, EXIT.VIOLATIONS, EXIT.OK].find((c) => codes.includes(c))
  console.log('')
  console.log(
    worst === EXIT.OK
      ? 'design-grid: all three layers clean'
      : `design-grid: failing with exit code ${worst}`
  )
  return worst
}

if (import.meta.url === pathToFileURL(process.argv[1]).href) {
  process.exitCode = await checkDesign()
}

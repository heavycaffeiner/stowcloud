// tools/design-grid/audit.mjs — the runtime layer's driver.
//
// Starts `vite dev` (mode development, so .env.development applies and
// VITE_API_MOCK=1 selects the in-memory backend), drives Chromium over
// pages x viewports x themes x locales, and runs the in-page collector on each.
//
// The dev server rather than the production build on purpose: .env.development
// is never applied by `npm run build`, and adding a mock switch to the shipping
// bundle to satisfy an audit is a worse trade than auditing the dev server.
// Vite serves the same CSS through the same functionsMixins plugin in both
// modes, so the geometry measured here is the geometry that ships.
//
// Console output and an exit code are the entire reporting surface. Nothing is
// uploaded, so a local run and a CI run say exactly the same thing.

import { Buffer } from 'node:buffer'
import { spawn, spawnSync } from 'node:child_process'
import { mkdirSync, readFileSync, rmSync, writeFileSync } from 'node:fs'
import { createRequire } from 'node:module'
import path from 'node:path'
import process from 'node:process'
import { fileURLToPath, pathToFileURL } from 'node:url'
import { chromium } from 'playwright'
import { auditDocument } from './collect.js'
import { loadPolicy } from './policy.mjs'
import { deadWaivers, isWaived, sharedWaivers, waiversFor } from './waivers.mjs'
import { scenarios } from './scenarios.mjs'

const require = createRequire(import.meta.url)
const WEB_ROOT = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..', '..')

export const EXIT = { OK: 0, VIOLATIONS: 1, CONFIG: 2, HARNESS: 3 }

// SGR escapes. Built with fromCharCode rather than written literally: an ESC
// byte in a source file is invisible in a diff and in review, so a pattern
// that has lost it looks identical to one that has not.
const ANSI_RE = new RegExp(`${String.fromCharCode(27)}\\[[0-9;]*m`, 'g')

const CATALOGUE = {
  en: require(path.join(WEB_ROOT, 'src/lib/i18n/en.json')),
  ko: require(path.join(WEB_ROOT, 'src/lib/i18n/ko.json'))
}

/** The app's own `t`, so a scenario addresses controls by the name they render. */
function translator(locale) {
  return (key, params) => {
    let s = CATALOGUE[locale][key] ?? CATALOGUE.ko[key] ?? key
    if (params) for (const [k, v] of Object.entries(params)) s = s.replaceAll(`{${k}}`, String(v))
    return s
  }
}

class HarnessError extends Error {}

// A 1x1 transparent PNG, served for the mock's download links.
//
// `mock.ts`'s `link()` hands back `/mock-download/<name>` -- a real same-origin
// URL by design, so a component can follow it -- but nothing serves those bytes
// and vite answers 404. The preview dialog's <img> then logs a console error,
// which the guard in auditOne treats as a page fault. Fulfilling the request
// keeps that guard strict and lets the preview render something real; the box
// it occupies is CSS-driven, so a 1x1 source measures the same as any other.
const STUB_PNG = Buffer.from(
  'iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mP8z8BQDwAEhQGAhKmMIQAAAABJRU5ErkJggg==',
  'base64'
)

/** Surfaces the matrix cannot reach, printed at the end of every run. */
const UNCOVERED = [
  'job tray: every mock job is terminal on its first poll (mock.ts makeMockJob), so the tray never renders'
]

// Every wait in this file is bounded. An audit that hangs is worse than one
// that fails: it holds a browser, a dev server and a CI runner, and it reports
// nothing at all about the thing it was measuring.
const TIMEOUT = {
  /** One page audit, end to end. Generous: a scenario is several clicks. */
  audit: 90_000,
  /** A single locator wait inside a scenario. */
  locator: 15_000,
  goto: 30_000,
  ready: 20_000,
  /** The in-page collector. It walks the DOM once; 30s is a hang, not slow. */
  collect: 30_000,
  settle: 5_000,
  /** Per path during warm-up, where a failure is expected and ignored. */
  warm: 10_000,
  close: 15_000,
  /** The whole matrix. The CI job allows 30 minutes; this fails first. */
  run: 20 * 60_000
}

/** Rejects with a HarnessError if `promise` has not settled within `ms`. */
function withTimeout(promise, ms, message) {
  let timer
  const capped = new Promise((_, reject) => {
    timer = setTimeout(() => reject(new HarnessError(message)), ms)
  })
  return Promise.race([promise, capped]).finally(() => clearTimeout(timer))
}

/** Runs `fn` over `items` with at most `limit` in flight, preserving order. */
async function mapLimit(items, limit, fn) {
  const results = new Array(items.length)
  let next = 0
  const workers = Array.from({ length: Math.min(limit, items.length) }, async () => {
    for (;;) {
      const i = next
      next += 1
      if (i >= items.length) return
      results[i] = await fn(items[i], i)
    }
  })
  await Promise.all(workers)
  return results
}

// ── dev server ────────────────────────────────────────────────────────────

const PID_FILE = path.join(WEB_ROOT, 'node_modules', '.cache', 'design-grid-vite.pid')

/**
 * Kills the dev server a previous run left behind.
 *
 * A vite dev server does not die with its parent, and the parent does not
 * always get a signal: a CI step timeout, or an editor killing the shell,
 * terminates this process outright on Windows. The pid on disk bounds that to
 * one orphan ever instead of one per interrupted run, which is what filled the
 * port range during development.
 */
function reapPreviousServer() {
  let pid
  try {
    pid = Number.parseInt(readFileSync(PID_FILE, 'utf8').trim(), 10)
  } catch {
    return
  }
  if (Number.isInteger(pid) && pid > 0) {
    if (process.platform === 'win32') {
      spawnSync('taskkill', ['/pid', String(pid), '/T', '/F'], { stdio: 'ignore' })
    } else {
      try {
        process.kill(-pid, 'SIGKILL')
      } catch {
        /* already gone */
      }
    }
  }
  rmSync(PID_FILE, { force: true })
}

async function startDevServer() {
  reapPreviousServer()
  const child = spawn(
    process.execPath,
    [
      path.join(WEB_ROOT, 'node_modules/vite/bin/vite.js'),
      'dev',
      '--config',
      'tools/design-grid/vite.audit.config.ts'
    ],
    {
      cwd: WEB_ROOT,
      stdio: ['ignore', 'pipe', 'pipe'],
      // Plain banner, so the URL below is parsed out of text rather than out
      // of colour codes. Both are set because vite honours NO_COLOR and
      // FORCE_COLOR independently, and a parent shell that exports
      // FORCE_COLOR=1 otherwise turns colour back on.
      env: { ...process.env, NO_COLOR: '1', FORCE_COLOR: '0' },
      // Its own process group, so killTree can signal the group. Not on
      // Windows, where detaching opens a console window instead.
      detached: process.platform !== 'win32'
    }
  )

  const url = await new Promise((resolve, reject) => {
    const timer = setTimeout(
      () => reject(new HarnessError('vite dev did not print a local URL within 60s')),
      60_000
    )
    let buffered = ''
    const onData = (chunk) => {
      // Vite colours its banner, and the escape sequences land between the
      // colon and the port digits, so the URL is only matchable once stripped.
      buffered += chunk.toString().replace(ANSI_RE, '')
      const m = /(http:\/\/(?:localhost|127\.0\.0\.1):\d+)\/?/.exec(buffered)
      if (!m) return
      clearTimeout(timer)
      resolve(m[1])
    }
    child.stdout.on('data', onData)
    child.stderr.on('data', onData)
    child.on('exit', (code) => {
      clearTimeout(timer)
      reject(new HarnessError(`vite dev exited with code ${code} before serving\n${buffered}`))
    })
  })

  // Vite dev servers do not die with their parent. If this process is killed
  // between here and stop() -- a CI timeout, a Ctrl-C, an editor killing the
  // shell -- the server is orphaned and keeps its port and its file watchers.
  // Six of them accumulated during development and the next run could not get
  // a port. Registering the teardown on exit makes the common cases clean up.
  mkdirSync(path.dirname(PID_FILE), { recursive: true })
  writeFileSync(PID_FILE, String(child.pid))

  const teardown = () => {
    rmSync(PID_FILE, { force: true })
    killTree(child)
  }
  process.once('exit', teardown)
  process.once('SIGINT', () => {
    teardown()
    process.exit(130)
  })
  process.once('SIGTERM', () => {
    teardown()
    process.exit(143)
  })

  return {
    url,
    async stop() {
      process.off('exit', teardown)
      await killTree(child)
    }
  }
}

/**
 * Kills a child and everything it spawned.
 *
 * `child.kill()` alone leaves vite's own workers behind: on Windows it
 * terminates only the named process, and on POSIX the child is its own process
 * group leader only if it was detached. `taskkill /T` and a process-group
 * signal are the respective ways to take the whole tree.
 */
function killTree(child) {
  // The pipes are what keep the event loop alive once the child is gone, and
  // a listener still attached to them keeps the whole closure alive with it.
  const release = () => {
    child.stdout?.removeAllListeners()
    child.stderr?.removeAllListeners()
    child.stdout?.destroy()
    child.stderr?.destroy()
    child.unref()
  }

  if (child.exitCode !== null || child.signalCode !== null) {
    release()
    return Promise.resolve()
  }

  const exited = new Promise((r) => child.once('exit', r))
  if (process.platform === 'win32') {
    spawnSync('taskkill', ['/pid', String(child.pid), '/T', '/F'], { stdio: 'ignore' })
  } else {
    try {
      process.kill(-child.pid, 'SIGTERM')
    } catch {
      child.kill('SIGTERM')
    }
  }

  // Bounded. taskkill can fail -- a pid already reaped, a refusal -- and then
  // the `exit` event never arrives. Waiting forever for a process we have
  // already asked to die is worse than leaking it: the reaper at the next
  // start cleans up a leak, and nothing cleans up a hang.
  const giveUp = new Promise((r) => setTimeout(r, 5_000).unref())
  return Promise.race([exited, giveUp]).then(release)
}

// ── one page audit ────────────────────────────────────────────────────────

async function auditOne(page, spec, policy, runtimeWaivers, t) {
  const { pageSpec, viewport, theme, locale, baseUrl } = spec
  const label = `${pageSpec.id} ${viewport.name} ${theme} ${locale}`

  const problems = []
  const onConsole = (msg) => {
    if (msg.type() === 'error') problems.push(`console.error: ${msg.text()}`)
  }
  const onPageError = (err) => problems.push(`pageerror: ${err.message}`)
  page.on('console', onConsole)
  page.on('pageerror', onPageError)

  try {
    await page.setViewportSize({ width: viewport.width, height: viewport.height })
    await page.goto(`${baseUrl}${pageSpec.path}`, {
      waitUntil: 'domcontentloaded',
      timeout: TIMEOUT.goto
    })

    const scenario = pageSpec.scenario ? scenarios[pageSpec.scenario] : null
    if (pageSpec.scenario && !scenario) {
      throw new HarnessError(`${label}: policy names scenario "${pageSpec.scenario}", which does not exist`)
    }

    await page
      .locator(pageSpec.ready)
      .first()
      .waitFor({ state: 'visible', timeout: TIMEOUT.ready })
      .catch(() => {
        throw new HarnessError(`${label}: never rendered "${pageSpec.ready}"`)
      })

    if (scenario) {
      await scenario.run(page, t).catch((e) => {
        // First line only: Playwright's own message carries a retry log
        // hundreds of lines long, and the first line is the actual cause.
        throw new HarnessError(
          `${label}: scenario "${pageSpec.scenario}" could not run: ${e.message.split('\n')[0]}`
        )
      })
      await page
        .locator(scenario.ready)
        .first()
        .waitFor({ state: 'visible', timeout: TIMEOUT.ready })
        .catch(() => {
          throw new HarnessError(
            `${label}: scenario "${pageSpec.scenario}" never reached "${scenario.ready}"`
          )
        })
    }

    await settle(page)

    const result = await withTimeout(
      page.evaluate(auditDocument, { policy, waivers: runtimeWaivers }),
      TIMEOUT.collect,
      `${label}: the in-page collector did not return within ${TIMEOUT.collect / 1000}s`
    )

    if (result.selectorErrors.length > 0) {
      const e = new Error(result.selectorErrors.join('\n'))
      e.isConfig = true
      throw e
    }
    if (result.elements < pageSpec.minElements) {
      // A page that failed to render has no boxes, therefore no violations,
      // therefore a clean pass. This floor is what stops that reading as one.
      throw new HarnessError(
        `${label}: collected ${result.elements} elements, below the floor of ${pageSpec.minElements}`
      )
    }
    if (problems.length > 0) {
      throw new HarnessError(`${label}: the page reported errors\n      ${problems.join('\n      ')}`)
    }

    return { label, ...result }
  } finally {
    page.off('console', onConsole)
    page.off('pageerror', onPageError)
  }
}

/**
 * Visits each distinct path once, discarding everything.
 *
 * Vite's dependency pre-bundling is lazy: the first visit to a route that pulls
 * in a module it has not optimized yet triggers a re-optimization, and every
 * request already in flight comes back 504 "Outdated Optimize Dep". That is a
 * dev-server artifact, not a page fault, but it is indistinguishable from one
 * at the console. Paying for it up front keeps the console.error guard strict.
 */
async function warmUp(page, baseUrl, pages) {
  const seen = new Set()
  for (const p of pages) {
    if (seen.has(p.path)) continue
    seen.add(p.path)
    await page
      .goto(`${baseUrl}${p.path}`, { waitUntil: 'domcontentloaded', timeout: TIMEOUT.warm })
      .catch(() => {})
    await page
      .locator(p.ready)
      .first()
      .waitFor({ state: 'visible', timeout: TIMEOUT.warm })
      .catch(() => {})
  }
}

/**
 * Waits for fonts and two frames, so nothing is measured mid-layout.
 *
 * Both waits are capped inside the page as well as outside it.
 * `requestAnimationFrame` never fires in a page the browser considers hidden,
 * and `document.fonts.ready` does not resolve while a face is still being
 * fetched, so an uncapped settle is the easiest way to hang this whole tool.
 * Measuring one frame early is a far smaller error than not measuring at all.
 */
async function settle(page) {
  await withTimeout(
    page.evaluate(async (capMs) => {
      const frames = new Promise((r) => requestAnimationFrame(() => requestAnimationFrame(r)))
      const fonts = document.fonts ? document.fonts.ready : Promise.resolve()
      const cap = new Promise((r) => setTimeout(r, capMs))
      await Promise.race([Promise.all([frames, fonts]), cap])
    }, TIMEOUT.settle - 1000),
    TIMEOUT.settle,
    'settle did not return'
  ).catch(() => {})
}

// ── the run ───────────────────────────────────────────────────────────────

/**
 * Runs the whole matrix.
 * @param {{only?: string, headed?: boolean, workers?: number, shards?: number}} opts
 *   `only` filters by page id; `shards` splits each theme/locale cell that many
 *   ways; `workers` caps how many shards run at once.
 * @returns {Promise<number>} the process exit code
 */
export async function runAudit(opts = {}) {
  const policy = loadPolicy()
  const rt = policy.runtime

  let set
  try {
    set = sharedWaivers()
  } catch (e) {
    console.error(`design-grid: ${e.message}`)
    return EXIT.CONFIG
  }
  const runtimeWaivers = waiversFor(set, 'runtime').map((w) => ({
    id: w.id,
    check: w.check,
    selector: w.selector,
    subtree: w.subtree
  }))

  const pages = opts.only ? rt.pages.filter((p) => p.id === opts.only) : rt.pages
  if (pages.length === 0) {
    console.error(`design-grid: no page matches --only=${opts.only}`)
    return EXIT.CONFIG
  }

  // One shard is one browser context with one page. A context is the unit of
  // parallelism rather than a page because theme and locale live in
  // localStorage, which pages in the same context share: two pages clearing and
  // re-seeding the same origin's storage would race and each would occasionally
  // render in the other's theme.
  const shardsPerCell = Math.max(1, opts.shards ?? 2)
  const shards = []
  for (const theme of rt.themes) {
    for (const locale of rt.locales) {
      const jobs = []
      for (const viewport of rt.viewports) for (const pageSpec of pages) jobs.push({ viewport, pageSpec })
      for (let s = 0; s < shardsPerCell; s += 1) {
        const mine = jobs.filter((_, i) => i % shardsPerCell === s)
        if (mine.length > 0) shards.push({ theme, locale, jobs: mine })
      }
    }
  }

  const workers = Math.max(1, Math.min(opts.workers ?? 6, shards.length))

  let server
  let browser
  const violations = []
  let unchecked = 0
  let audits = 0

  try {
    server = await startDevServer()
    browser = await chromium.launch({ headless: !opts.headed })

    // One serial pass before anything is measured, so vite's lazy dependency
    // pre-bundling settles while nothing depends on the console being quiet.
    {
      const warmContext = await browser.newContext()
      const warmPage = await warmContext.newPage()
      await warmUp(warmPage, server.url, pages)
      await warmContext.close()
    }

    const total = shards.reduce((n, s) => n + s.jobs.length, 0)
    let done = 0
    console.log(
      `design-grid runtime: ${total} page audits across ${shards.length} shards, ${workers} at a time`
    )

    const runShard = async (shard) => {
      const context = await browser.newContext()
      try {
        await context.route('**/mock-download/**', (route) =>
          route.fulfill({ status: 200, contentType: 'image/png', body: STUB_PNG })
        )
        // Cleared on every navigation, not just the first. The app persists
        // view mode, density, drawer and details state under sc.* keys, so
        // without this the grid toggle a scenario flips stays flipped for
        // every page audited after it and the matrix stops describing itself.
        await context.addInitScript(
          ([th, lo]) => {
            localStorage.clear()
            localStorage.setItem('sc.theme', th)
            localStorage.setItem('sc.locale', lo)
          },
          [shard.theme, shard.locale]
        )
        const t = translator(shard.locale)
        const page = await context.newPage()
        // Caps every locator wait a scenario makes. Without it one missing
        // control costs Playwright's 30s default, several times over.
        page.setDefaultTimeout(TIMEOUT.locator)

        const out = []
        for (const { viewport, pageSpec } of shard.jobs) {
          const spec = { pageSpec, viewport, theme: shard.theme, locale: shard.locale, baseUrl: server.url }
          const label = `${pageSpec.id} ${viewport.name} ${shard.theme} ${shard.locale}`
          out.push(
            await withTimeout(
              auditOne(page, spec, policy, runtimeWaivers, t),
              TIMEOUT.audit,
              `${label}: the audit did not finish within ${TIMEOUT.audit / 1000}s`
            )
          )
          // One line per audit, on stderr so it never mixes into the violation
          // report on stdout. Without it a slow run and a hung one look the
          // same from outside, which cost an afternoon.
          done += 1
          process.stderr.write(`  [${String(done).padStart(3)}/${total}] ${label}\n`)
        }
        return out
      } finally {
        await withTimeout(context.close(), TIMEOUT.close, 'closing a context timed out').catch(() => {})
      }
    }

    const perShard = await withTimeout(
      mapLimit(shards, workers, runShard),
      TIMEOUT.run,
      `the run did not finish within ${TIMEOUT.run / 60_000} minutes`
    )

    // Folded after the fact rather than inside the workers: `set.used` and the
    // violation list are shared state, and keeping the mutation single-threaded
    // is what makes the report identical whatever order the shards finished in.
    for (const r of perShard.flat()) {
      audits += 1
      unchecked += r.unchecked
      for (const id of r.usedWaiverIds) set.used.add(id)
      for (const v of r.violations) {
        if (isWaived(set, v)) continue
        violations.push({ ...v, where: r.label })
      }
    }

    // Reported here rather than after the `finally`, so a slow or stuck
    // teardown can delay the process exiting but can never withhold the
    // answer the run spent its time computing.
    violations.sort((a, b) => a.where.localeCompare(b.where) || a.check.localeCompare(b.check))
    // A partial run cannot tell a dead waiver from one whose page it skipped,
    // so it does not get to make that call.
    const dead = opts.only ? [] : deadWaivers(set, ['runtime'])
    return reportRuntime({ violations, unchecked, audits, dead, partial: Boolean(opts.only) })
  } catch (e) {
    console.error(`design-grid: ${e.message}`)
    return e.isConfig ? EXIT.CONFIG : EXIT.HARNESS
  } finally {
    if (browser) {
      await withTimeout(browser.close(), TIMEOUT.close, 'closing the browser timed out').catch(() => {})
    }
    if (server) {
      await withTimeout(server.stop(), TIMEOUT.close, 'stopping the dev server timed out').catch(() => {})
    }
  }
}

function reportRuntime({ violations, unchecked, audits, dead, partial }) {
  for (const v of violations) {
    console.log(`runtime  ${v.where}  ${v.check}`)
    console.log(`  ${v.selector}`)
    if (v.offender) console.log(`  offender: ${v.offender}`)
    console.log(`  ${v.actual}; expected ${v.expected}`)
  }

  const byCheck = new Map()
  for (const v of violations) byCheck.set(v.check, (byCheck.get(v.check) ?? 0) + 1)

  console.log('')
  console.log(`design-grid runtime: ${audits} page audits, ${violations.length} violations`)
  for (const [check, n] of [...byCheck].sort((a, b) => b[1] - a[1])) {
    console.log(`  ${check.padEnd(20)} ${n}`)
  }
  if (unchecked > 0) {
    console.log(`  ${'(unchecked)'.padEnd(20)} ${unchecked} baseline-aligned containers`)
  }
  // Stated on every run, clean or not: a bound on coverage that is only
  // recorded in a comment reads as "everything was checked" from the log.
  for (const gap of UNCOVERED) console.log(`  not audited: ${gap}`)
  if (partial) console.log('  --only was set: this is one page, and no waiver was swept')

  if (dead.length > 0) {
    console.log('')
    for (const w of dead) {
      console.log(`design-grid: waiver "${w.id}" matched nothing. Remove it or fix its selector.`)
    }
    return EXIT.CONFIG
  }

  return violations.length > 0 ? EXIT.VIOLATIONS : EXIT.OK
}

// ── CLI ───────────────────────────────────────────────────────────────────

if (import.meta.url === pathToFileURL(process.argv[1]).href) {
  const args = process.argv.slice(2)
  const num = (flag) => {
    const raw = args.find((a) => a.startsWith(`${flag}=`))?.slice(flag.length + 1)
    return raw === undefined ? undefined : Number.parseInt(raw, 10)
  }
  process.exitCode = await runAudit({
    only: args.find((a) => a.startsWith('--only='))?.slice('--only='.length),
    headed: args.includes('--headed'),
    workers: num('--workers'),
    shards: num('--shards')
  })
}

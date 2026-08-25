// A real browser against a real server.
//
// Every other test in this tree drives a function. This one loads the shipped
// interface in Chromium and signs in the way a person does, which is the only
// check that would have caught login being mounted on the wrong path: the
// handler was correct, its tests passed, and nothing could reach it.
//
// Run: node e2e/session.spec.mjs <base-url> <setup-token>
import { chromium } from 'playwright'

const BASE = process.argv[2] ?? 'https://127.0.0.1:18900'
const TOKEN = process.argv[3] ?? ''
const USER = 'e2e-admin'
const PASSWORD = 'correct-horse-battery-staple'

let failures = 0

function check(name, ok, detail = '') {
  const mark = ok ? 'pass' : 'FAIL'
  if (!ok) failures += 1
  console.log(`  ${mark}  ${name}${detail ? `: ${detail}` : ''}`)
}

/** The API as the browser sees it: same origin, same cookies, same guard. */
async function api(page, method, path, body) {
  return page.evaluate(
    async ([m, p, b]) => {
      const res = await fetch(p, {
        method: m,
        headers: b ? { 'Content-Type': 'application/json' } : {},
        body: b ? JSON.stringify(b) : undefined
      })
      let parsed = null
      const text = await res.text()
      try {
        parsed = text ? JSON.parse(text) : null
      } catch {
        parsed = text
      }
      return { status: res.status, body: parsed }
    },
    [method, path, body ?? null]
  )
}

const browser = await chromium.launch()
// The certificate is generated into the server's own data directory and this
// is loopback, so there is nothing to verify against. That is a statement
// about this harness rather than about the server.
const context = await browser.newContext({ ignoreHTTPSErrors: true })
const page = await context.newPage()

// Anything the browser refused to run or fetch. A blocked script is not a
// failed request and not a bad status: it is a console line and nothing else,
// so without collecting these a blank page looks like a healthy one.
const violations = []
page.on('console', m => {
  const t = m.text()
  if (m.type() === 'error' && /Content Security Policy/i.test(t)) violations.push(t)
})
page.on('pageerror', e => violations.push('pageerror: ' + e.message))

try {
  console.log('the interface loads')
  const res = await page.goto(BASE, { waitUntil: 'domcontentloaded' })
  check('the document is served', res?.status() === 200, `status ${res?.status()}`)
  const bundle = await page.evaluate(() => document.documentElement.innerHTML)
  check('the embedded bundle is referenced', bundle.includes('app/immutable/'),
    bundle.includes('app/immutable/') ? '' : bundle.slice(0, 80))

  // The bundle referenced is not the bundle running. Everything below drives
  // the API with fetch, which no content policy stops, so the whole suite
  // passed against a server whose policy blocked the framework's inline
  // bootstrap: every request succeeded and the screen was blank.
  //
  // This is the one check that fails when the interface does not start.
  await page.waitForFunction(() => (document.body?.innerText ?? '').trim().length > 0,
    null, { timeout: 15000 }).catch(() => {})
  const rendered = (await page.evaluate(() => (document.body?.innerText ?? '').trim()))
  check('the interface renders something', rendered.length > 0,
    rendered.length > 0 ? '' : 'the page is blank; the app never started')
  check('nothing was blocked by the content policy', violations.length === 0,
    violations.join(' | '))

  console.log('first run')
  const setupState = await api(page, 'GET', '/api/setup')
  check('the setup question answers the field the client reads',
    typeof setupState.body?.required === 'boolean',
    JSON.stringify(setupState.body))

  if (setupState.body?.required && TOKEN) {
    const created = await api(page, 'POST', '/api/setup', {
      token: TOKEN,
      username: USER,
      password: PASSWORD
    })
    check('the administrator is created', created.status === 200 || created.status === 201,
      `status ${created.status}`)
  }

  console.log('signing in')
  const noSession = await api(page, 'GET', '/api/auth/session')
  check('no credential is refused as required rather than invalid',
    noSession.status === 401 && noSession.body?.error?.code === 'auth.required',
    JSON.stringify(noSession.body))

  const login = await api(page, 'POST', '/api/auth/login', {
    username: USER,
    password: PASSWORD
  })
  check('signing in succeeds on the path the client calls', login.status === 200,
    `status ${login.status} ${JSON.stringify(login.body)}`)

  const wrong = await api(page, 'POST', '/api/auth/login', {
    username: USER,
    password: 'not-the-password'
  })
  check('a wrong password is refused', wrong.status === 401, `status ${wrong.status}`)

  const session = await api(page, 'GET', '/api/auth/session')
  check('the session is established', session.status === 200, `status ${session.status}`)
  const csrf = session.body?.csrf ?? ''
  check('the session carries a token for state-changing requests', csrf.length > 0)

  console.log('browsing')
  // The first administrator is granted every configured share at setup, so
  // this account can read the one the fixture serves. Without that a fresh
  // deployment is a dead end: a share is only reachable through a grant, the
  // screen that creates grants is behind the interface, and the first run has
  // none.
  const list = await api(page, 'GET', '/api/fs/list?path=docs')
  check('the first administrator can read the configured share', list.status === 200,
    `status ${list.status}`)

  // The client's own path spelling is rooted, and it has to be accepted: its
  // URLs are rooted, so this is what every request the interface makes looks
  // like. It answered 422 for the whole of the port, which is a server that
  // cannot list a directory.
  const rooted = await api(page, 'GET', '/api/fs/list?path=/docs')
  check('a rooted path is the same path', rooted.status === 200, `status ${rooted.status}`)

  // The windowed fetch the virtual scroller makes: it names the directory as
  // `listing`, not `path`, and asks for a slice by index. It was refused as a
  // malformed path for the whole of the port, so every row past the first page
  // stayed a placeholder no matter how far anybody scrolled.
  const windowed = await api(page, 'GET',
    `/api/fs/list?listing=${encodeURIComponent(list.body?.listing ?? 'docs')}&offset=0&limit=1`)
  check('a windowed fetch addresses the listing it was handed',
    windowed.status === 200, `status ${windowed.status}`)
  check('the window is the size that was asked for',
    windowed.body?.entries?.length === 1, `entries=${windowed.body?.entries?.length}`)
  // The token the client last saw. A matching one is not stale; the response
  // omits the flag rather than sending false.
  const fresh = await api(page, 'GET',
    `/api/fs/list?listing=${encodeURIComponent(list.body?.listing ?? 'docs')}&offset=0&limit=1` +
    `&dir_etag=${encodeURIComponent(list.body?.dir_etag ?? '')}`)
  check('an unchanged directory is not reported stale',
    fresh.status === 200 && !fresh.body?.stale, `stale=${fresh.body?.stale}`)
  const moved = await api(page, 'GET',
    `/api/fs/list?listing=${encodeURIComponent(list.body?.listing ?? 'docs')}&offset=0&limit=1&dir_etag=not-the-one`)
  check('a directory that moved under the window says so',
    moved.status === 200 && moved.body?.stale === true, `stale=${moved.body?.stale}`)

  // The existence rule: a path that does not exist and one this account may
  // not see answer identically, so a stranger cannot probe for what is there.
  const missing = await api(page, 'GET', '/api/fs/list?path=docs/no-such-directory')
  const outside = await api(page, 'GET', '/api/fs/list?path=secret/anything')
  check('a missing path and a forbidden one are indistinguishable',
    missing.status === 404 &&
      outside.status === 404 &&
      JSON.stringify(missing.body) === JSON.stringify(outside.body),
    `${missing.status}/${outside.status}`)

  console.log('the surfaces the admin screens call')
  const surfaces = [
    ['GET', '/api/admin/users'],
    ['GET', '/api/admin/groups'],
    ['GET', '/api/admin/grants'],
    ['GET', '/api/admin/storage'],
    ['GET', '/api/admin/audit'],
    ['GET', '/api/admin/shares'],
    ['GET', '/api/admin/server-settings'],
    ['GET', '/api/admin/index/settings'],
    ['GET', '/api/auth/app-passwords'],
    ['GET', '/api/auth/totp/recovery-codes'],
    ['GET', '/api/jobs'],
    ['GET', '/api/shares'],
    ['GET', '/api/health']
  ]
  for (const [method, path] of surfaces) {
    const r = await api(page, method, path)
    // What is being checked is that the route exists and the request reached a
    // handler. A subsystem that is not built answers that it is not
    // implemented, which is a status a client can act on; a route that is not
    // mounted answers 404, which is the defect this whole run exists for.
    check(`${method} ${path} reaches a handler`,
      r.status !== 404 && r.status !== 405,
      `status ${r.status}`)
  }

  console.log('a state-changing request needs its token')
  const noToken = await page.evaluate(async () => {
    const res = await fetch('/api/admin/groups', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ name: 'forged' })
    })
    return res.status
  })
  check('a request without the token is refused', noToken === 400 || noToken === 403,
    `status ${noToken}`)

  // A fresh name per run, because a name another row holds is a conflict and
  // this check is about the token rather than about the name.
  const group = `e2e-group-${Date.now()}`
  const withToken = await page.evaluate(
    async ([token, name]) => {
      const res = await fetch('/api/admin/groups', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json', 'Sc-Csrf': token },
        body: JSON.stringify({ name })
      })
      return { status: res.status, body: await res.json().catch(() => null) }
    },
    [csrf, group]
  )
  check('a request with the token is accepted', withToken.status === 201,
    `status ${withToken.status}`)

  console.log('signing out')
  const out = await page.evaluate(
    async ([token]) => {
      const res = await fetch('/api/auth/logout', {
        method: 'POST',
        headers: { 'Sc-Csrf': token }
      })
      return res.status
    },
    [csrf]
  )
  check('signing out succeeds', out === 200 || out === 204, `status ${out}`)
  const after = await api(page, 'GET', '/api/auth/session')
  check('the session is gone afterwards', after.status === 401, `status ${after.status}`)
} finally {
  await browser.close()
}

console.log(failures === 0 ? '\nall checks passed' : `\n${failures} checks failed`)
process.exit(failures === 0 ? 0 : 1)

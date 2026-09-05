// A real browser against a real server.
//
// Every other test in this tree drives a function. This one loads the shipped
// interface in Chromium and signs in the way a person does, which is the only
// check that would have caught login being mounted on the wrong path: the
// handler was correct, its tests passed, and nothing could reach it.
//
// Run: node e2e/session.spec.mjs <base-url> <setup-token> <share-path>
import { chromium } from 'playwright'

const BASE = process.argv[2] ?? 'https://127.0.0.1:18900'
const TOKEN = process.argv[3] ?? ''
// The folder the fixture serves, as the server sees it. Nothing declares a
// share any more, so the suite creates one.
const SHARE = process.argv[4] ?? ''
const USER = 'e2e-admin'
const PASSWORD = 'correct-horse-battery-staple'

let failures = 0

function check(name, ok, detail = '') {
  const mark = ok ? 'pass' : 'FAIL'
  if (!ok) failures += 1
  console.log(`  ${mark}  ${name}${detail ? `: ${detail}` : ''}`)
}

/** The API as the browser sees it: same origin, same cookies, same guard. */
async function api(page, method, path, body, csrf) {
  return page.evaluate(
    async ([m, p, b, token]) => {
      const headers = {}
      if (b) headers['Content-Type'] = 'application/json'
      if (token) headers['Sc-Csrf'] = token
      const res = await fetch(p, {
        method: m,
        headers,
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
    [method, path, body ?? null, csrf ?? '']
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

// The one exception, and it is the harness rather than the product: this run
// serves a certificate it minted itself, and Chromium refuses to fetch a
// Service Worker script over an untrusted certificate whatever the page-level
// override says. The app already treats a failed registration as "no worker",
// falling back to a buffered download, so the refusal is the expected
// behaviour here and not a content-policy violation. Matched narrowly, by
// both the registration and the certificate, so a real worker error still
// fails this check.
const isSelfSignedWorkerRefusal = (text) =>
  /register a ServiceWorker/i.test(text) && /certificate/i.test(text)

page.on('console', m => {
  const t = m.text()
  if (m.type() === 'error' && /Content Security Policy/i.test(t)) violations.push(t)
})
page.on('pageerror', e => {
  if (isSelfSignedWorkerRefusal(e.message)) return
  violations.push('pageerror: ' + e.message)
})

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
  const setupState = await api(page, 'GET', '/api/v1/system/setup')
  check('the setup question answers the field the client reads',
    typeof setupState.body?.required === 'boolean',
    JSON.stringify(setupState.body))

  if (setupState.body?.required && TOKEN) {
    const created = await api(page, 'POST', '/api/v1/system/setup', {
      token: TOKEN,
      username: USER,
      password: PASSWORD,
      // The form names what the server answers for. It is required: until it
      // is saved the host guard is in its first-boot mode, admitting the
      // local network on the strength of the address alone.
      app_hosts: [new URL(BASE).hostname],
      trusted_proxies: []
    })
    check('the administrator is created', created.status === 200 || created.status === 201,
      `status ${created.status} ${JSON.stringify(created.body).slice(0, 160)}`)
  }

  console.log('signing in')
  const noSession = await api(page, 'GET', '/api/v1/auth/session')
  // Refused as an address that is not there. Every route but the public ones
  // answers a credential-less request that way, so a stranger with a word
  // list cannot tell a real route from one that was never mounted.
  check('no credential is refused as a path that is not there',
    noSession.status === 404 && noSession.body?.error === 'request_failed',
    JSON.stringify(noSession.body))

  // The wrong password first, while there is no session. Once one exists this
  // is a state-changing request from an authenticated caller, so CSRF refuses
  // it with a 403 before the credential is ever read, and the check would be
  // asserting the guard rather than the refusal it names.
  const wrong = await api(page, 'POST', '/api/v1/auth/login', { login: USER, password: 'not-the-password' })
  check('a wrong password is refused', wrong.status === 401, `status ${wrong.status}`)

  const login = await api(page, 'POST', '/api/v1/auth/login', { login: USER, password: PASSWORD })
  check('signing in succeeds on the path the client calls', login.status === 200,
    `status ${login.status} ${JSON.stringify(login.body)}`)

  const session = await api(page, 'GET', '/api/v1/auth/session')
  check('the session is established', session.status === 200, `status ${session.status}`)
  const csrf = session.body?.csrf ?? ''
  check('the session carries a token for state-changing requests', csrf.length > 0)

  console.log('the first share')
  // A fresh deployment serves nothing: no file declares a folder, so the
  // first share is one the administrator creates from the interface.
  const existing = await api(page, 'GET', '/api/v1/admin/shares')
  const already = (Array.isArray(existing.body) ? existing.body : (existing.body?.shares ?? []))
    .some((s) => s.name === 'docs')
  if (!already && SHARE) {
    const made = await api(page, 'POST', '/api/v1/admin/shares',
      { name: 'docs', host: SHARE }, csrf)
    check('the first share is created from the interface', made.status === 201,
      `status ${made.status} ${JSON.stringify(made.body).slice(0, 160)}`)
  }

  console.log('browsing')
  // Registering a share grants its creator the whole tree under the share's
  // own name, so the administrator can already read it here. A second grant
  // naming the same subject, share, subpath and reach is refused as the
  // duplicate it is, which is what this run proves before browsing.
  //
  // The label matters: it is the name the share is addressed by. Without one
  // the root projects the grant as share-<id>, so every path a person types
  // is a 404 against a share that is definitely there.
  const shareRow = (await api(page, 'GET', '/api/v1/admin/shares')).body
  const docs = (Array.isArray(shareRow) ? shareRow : (shareRow?.shares ?? []))
    .find((s) => s.name === 'docs')
  check('the share this run browses exists', docs !== undefined,
    JSON.stringify(shareRow).slice(0, 160))
  if (docs) {
    const again = await api(page, 'POST', '/api/v1/admin/grants', {
      user: String(session.body?.user?.id ?? 1),
      share: String(docs.id),
      allow: ['read', 'download', 'write', 'create', 'delete'],
      inherit: true,
      label: 'docs'
    }, csrf)
    check('a second grant over the same target is refused',
      again.status === 409 && again.body?.error?.detail?.reason_key === 'admin.grant_exists',
      `status ${again.status} ${JSON.stringify(again.body).slice(0, 160)}`)
  }

  const list = await api(page, 'GET', '/api/v1/files/list?path=docs')
  check('the granted share is readable', list.status === 200, `status ${list.status}`)
  check('the listing carries the rows it found',
    Array.isArray(list.body?.entries) && list.body.entries.length > 0,
    JSON.stringify(list.body).slice(0, 160))

  // The client's own path spelling is rooted, and it has to be accepted: its
  // URLs are rooted, so this is what every request the interface makes looks
  // like. It answered 422 for the whole of the port, which is a server that
  // cannot list a directory.
  const rooted = await api(page, 'GET', '/api/v1/files/list?path=/docs')
  check('a rooted path is the same path', rooted.status === 200, `status ${rooted.status}`)

  // The window the grid draws. It asks for a bounded page and follows the
  // cursor the previous page ended with, which is null on the last one: an
  // absent cursor and a null one have to stay distinguishable, because the
  // pager reads null as "stop".
  const firstPage = await api(page, 'GET', '/api/v1/files/list?path=docs&limit=1')
  check('a bounded page is the size that was asked for',
    firstPage.body?.entries?.length === 1,
    `entries=${firstPage.body?.entries?.length}`)
  check('the page reports the total behind it',
    typeof firstPage.body?.total === 'number', JSON.stringify(firstPage.body).slice(0, 160))
  if (firstPage.body?.cursor) {
    const nextPage = await api(page, 'GET',
      `/api/v1/files/list?path=docs&limit=1&cursor=${encodeURIComponent(firstPage.body.cursor)}`)
    check('the cursor walks to the next page', nextPage.status === 200,
      `status ${nextPage.status}`)
    check('the second page is not the first',
      nextPage.body?.entries?.[0]?.name !== firstPage.body?.entries?.[0]?.name,
      `both pages start at ${nextPage.body?.entries?.[0]?.name}`)
  }

  // The change token the client conditions on, which is what lets it skip a
  // redraw when nothing moved.
  check('the listing carries a change token',
    typeof list.body?.dir_etag === 'string' && list.body.dir_etag.length > 0,
    `dir_etag=${list.body?.dir_etag}`)

  // The existence rule: a path that does not exist and one this account may
  // not see answer identically, so a stranger cannot probe for what is there.
  const missing = await api(page, 'GET', '/api/v1/files/list?path=docs/no-such-directory')
  const outside = await api(page, 'GET', '/api/v1/files/list?path=secret/anything')
  check('a missing path and a forbidden one are indistinguishable',
    missing.status === 404 &&
      outside.status === 404 &&
      JSON.stringify(missing.body) === JSON.stringify(outside.body),
    `${missing.status}/${outside.status}`)

  console.log('the surfaces the admin screens call')
  const surfaces = [
    ['GET', '/api/v1/admin/users'],
    ['GET', '/api/v1/admin/groups'],
    ['GET', '/api/v1/admin/grants'],
    ['GET', '/api/v1/admin/storage'],
    ['GET', '/api/v1/admin/audit'],
    ['GET', '/api/v1/admin/shares'],
    ['GET', '/api/v1/admin/settings'],
    ['GET', '/api/v1/account/app-passwords'],
    ['GET', '/api/v1/account/totp/recovery-codes'],
    ['GET', '/api/v1/jobs'],
    ['GET', '/api/v1/links'],
    ['GET', '/api/v1/system/health']
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
    const res = await fetch('/api/v1/admin/groups', {
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
      const res = await fetch('/api/v1/admin/groups', {
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
      const res = await fetch('/api/v1/auth/logout', {
        method: 'POST',
        headers: { 'Sc-Csrf': token }
      })
      return res.status
    },
    [csrf]
  )
  check('signing out succeeds', out === 200 || out === 204, `status ${out}`)
  const after = await api(page, 'GET', '/api/v1/auth/session')
  // The revoked session reaches nothing, and what it gets back is the same
  // answer a stranger gets: the address is not there.
  check('the session is gone afterwards', after.status === 404, `status ${after.status}`)
} finally {
  await browser.close()
}

console.log(failures === 0 ? '\nall checks passed' : `\n${failures} checks failed`)
process.exit(failures === 0 ? 0 : 1)

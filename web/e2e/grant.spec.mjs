// The grant path, end to end, in a real browser.
//
// An administrator creating a grant and the file listing that grant unlocks
// are two subsystems and one screen. Nothing before this checked that a grant
// written through the admin surface reaches the evaluator the file surface
// asks: the write went to the database and the process answering requests
// held the grants it loaded at startup.
//
// Run: node e2e/grant.spec.mjs <base-url> <user> <password>
import { chromium } from 'playwright'

const BASE = process.argv[2] ?? 'https://localhost:18900'
const USER = process.argv[3] ?? 'e2e-admin'
const PASSWORD = process.argv[4] ?? 'correct-horse-battery-staple'

let failures = 0
function check(name, ok, detail = '') {
  if (!ok) failures += 1
  console.log(`  ${ok ? 'pass' : 'FAIL'}  ${name}${detail ? `: ${detail}` : ''}`)
}

const browser = await chromium.launch()
const context = await browser.newContext({ ignoreHTTPSErrors: true })
const page = await context.newPage()

async function api(method, path, body, csrf) {
  return page.evaluate(
    async ([m, p, b, token]) => {
      const headers = {}
      if (b) headers['Content-Type'] = 'application/json'
      if (token) headers['Sc-Csrf'] = token
      const res = await fetch(p, { method: m, headers, body: b ? JSON.stringify(b) : undefined })
      const text = await res.text()
      let parsed = null
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

try {
  await page.goto(BASE, { waitUntil: 'domcontentloaded' })
  const login = await api('POST', '/api/auth/login', { username: USER, password: PASSWORD })
  check('signed in', login.status === 200, `status ${login.status}`)
  const session = await api('GET', '/api/auth/session')
  const csrf = session.body?.csrf ?? ''

  // The share the server was configured with.
  const shares = await api('GET', '/api/admin/shares')
  const shareList = Array.isArray(shares.body) ? shares.body : (shares.body?.shares ?? [])
  check('the configured share is listed', shareList.length > 0,
    JSON.stringify(shares.body).slice(0, 120))
  const shareID = shareList[0]?.id
  const shareName = shareList[0]?.name

  // Without a grant, the share is not readable. That is the existence rule
  // working, not a missing share.
  const before = await api('GET', `/api/fs/list?path=${shareName}`)
  check('an ungranted share is not readable', before.status === 404, `status ${before.status}`)

  // The grant, written through the surface the admin screen uses.
  const created = await api(
    'POST',
    '/api/admin/grants',
    {
      user: session.body?.user?.id,
      share: shareID,
      subpath: '',
      allow: ['read', 'write', 'create', 'delete', 'download'],
      deny: [],
      inherit: true,
      label: shareName
    },
    csrf
  )
  check('the grant is created', created.status === 201,
    `status ${created.status} ${JSON.stringify(created.body).slice(0, 140)}`)

  // The point of the whole test: the grant is live in the process answering
  // requests, without a restart.
  const after = await api('GET', `/api/fs/list?path=${shareName}`)
  check('the granted share lists immediately, with no restart', after.status === 200,
    `status ${after.status}`)
  check('the listing reports whether its change token is exact',
    typeof after.body?.dir_etag_weak === 'boolean',
    `dir_etag_weak=${after.body?.dir_etag_weak}`)
  check('the listing carries entries', Array.isArray(after.body?.entries),
    `${after.body?.entries?.length} entries`)

  // And an entry says whether its own token is exact, which is what the
  // editor's conflict handling branches on.
  const entry = after.body?.entries?.[0]
  if (entry) {
    check('an entry reports whether its change token is exact',
      typeof entry.etag_weak === 'boolean',
      `etag_weak=${entry.etag_weak}`)
  }

  // Removing it takes the access away again, also without a restart.
  const removed = await api('DELETE', `/api/admin/grants/${created.body?.id}`, null, csrf)
  check('the grant is removed', removed.status === 204, `status ${removed.status}`)
  const afterRemoval = await api('GET', `/api/fs/list?path=${shareName}`)
  check('access is gone immediately', afterRemoval.status === 404, `status ${afterRemoval.status}`)
} finally {
  await browser.close()
}

console.log(failures === 0 ? '\nall checks passed' : `\n${failures} checks failed`)
process.exit(failures === 0 ? 0 : 1)

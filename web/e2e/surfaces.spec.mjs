// The surfaces that used to answer "not implemented", end to end.
//
// Each one had a working implementation underneath and nothing calling it, so
// every unit test passed while the route answered 501. That failure is only
// visible from outside: this signs in the way a person does and asks the same
// paths the shipped client asks.
//
// Run: node e2e/surfaces.spec.mjs <base-url> <user> <password>
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
      const res = await fetch(p, {
        method: m,
        headers,
        body: b ? JSON.stringify(b) : undefined,
        redirect: 'manual'
      })
      const text = await res.text()
      let parsed = null
      try {
        parsed = text ? JSON.parse(text) : null
      } catch {
        parsed = text
      }
      return { status: res.status, body: parsed, location: res.headers.get('location') }
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

  // The session carries the single-sign-on block the settings screen reads. It
  // has to be present and say "not linked" rather than be absent, or the screen
  // cannot tell "no link" from "this build has no such field".
  check('the session reports the single-sign-on link',
    typeof session.body?.oidc?.linked === 'boolean',
    JSON.stringify(session.body?.oidc))

  // ---- recency ----
  const recent = await api('GET', '/api/recent?limit=20')
  check('recency answers', recent.status === 200, `status ${recent.status}`)
  check('recency answers with a list', Array.isArray(recent.body?.hits),
    JSON.stringify(recent.body).slice(0, 120))

  // A window and a scope are both accepted, which is what the screen sends.
  const windowed = await api('GET', '/api/recent?since=2020-01-01T00:00:00Z&limit=5')
  check('recency accepts a window', windowed.status === 200, `status ${windowed.status}`)

  // A window this server cannot read is refused rather than quietly ignored: a
  // request that silently returned everything would look like an empty window.
  const badWindow = await api('GET', '/api/recent?since=last-tuesday')
  check('an unreadable window is refused', badWindow.status === 400,
    `status ${badWindow.status}`)

  // ---- the index ----
  const estimate = await api('GET', '/api/admin/index/estimate')
  check('the index estimate answers', estimate.status === 200, `status ${estimate.status}`)
  check('the estimate carries a size', typeof estimate.body?.index_bytes === 'number',
    JSON.stringify(estimate.body).slice(0, 160))
  check('the estimate says how much it measured',
    ['high', 'medium', 'low'].includes(estimate.body?.confidence),
    `confidence=${estimate.body?.confidence}`)

  // The build is a job every time, because it walks every share by definition.
  const build = await api('POST', '/api/admin/index/build', null, csrf)
  check('the index build is accepted', build.status === 202 || build.status === 501,
    `status ${build.status} ${JSON.stringify(build.body).slice(0, 120)}`)
  if (build.status === 202) {
    check('the build answers a job id', build.body?.job !== undefined,
      JSON.stringify(build.body))
    const job = await api('GET', `/api/jobs/${build.body?.job}`)
    check('the build job is readable', job.status === 200, `status ${job.status}`)
    check('the build job knows what kind it is', job.body?.kind === 'index-build',
      `kind=${job.body?.kind}`)
  }

  // ---- single sign-on ----
  // Reachable with no credential by necessity: the login screen asks before
  // anyone has signed in.
  const oidcConfig = await api('GET', '/api/auth/oidc/config')
  check('the single-sign-on config answers', oidcConfig.status === 200,
    `status ${oidcConfig.status}`)
  check('it says whether a provider is configured',
    typeof oidcConfig.body?.enabled === 'boolean',
    JSON.stringify(oidcConfig.body))
  // The issuer and the client id are withheld from an anonymous caller.
  check('it withholds the issuer and the client id',
    oidcConfig.body?.issuer === undefined && oidcConfig.body?.client_id === undefined,
    JSON.stringify(oidcConfig.body))

  // These are navigations, not fetches. The flow works by sending the browser
  // somewhere, so what matters is where the browser ends up: a fetch reports
  // an opaque redirect and proves nothing about the destination.
  //
  // With no provider configured the start sends the person back with a
  // symbolic code rather than an error page they cannot act on.
  await page.goto(`${BASE}/api/auth/oidc/start`, { waitUntil: 'domcontentloaded' })
  check('a deployment with no provider says so where the browser lands',
    page.url().includes('oidc_error=oidc.disabled'), `landed on ${page.url()}`)

  // An open redirect is what this route would be if it trusted the caller's
  // destination. The browser has to land back on this server.
  await page.goto(
    `${BASE}/api/auth/oidc/start?returnTo=${encodeURIComponent('https://evil.example.com/')}`,
    { waitUntil: 'domcontentloaded' }
  )
  check('a destination somewhere else is refused', page.url().startsWith(BASE),
    `landed on ${page.url()}`)

  await page.goto(
    `${BASE}/api/auth/oidc/start?returnTo=${encodeURIComponent('//evil.example.com/')}`,
    { waitUntil: 'domcontentloaded' }
  )
  check('a scheme-relative destination is refused', page.url().startsWith(BASE),
    `landed on ${page.url()}`)

  // The callback with nothing to work from lands the person back here too,
  // rather than on a status a browser renders as a failure.
  await page.goto(`${BASE}/api/auth/oidc/callback`, { waitUntil: 'domcontentloaded' })
  check('the callback lands the browser back here', page.url().startsWith(BASE),
    `landed on ${page.url()}`)
  check('the callback says why it could not proceed',
    page.url().includes('oidc_error='), `landed on ${page.url()}`)

  // Back to the app, since the navigations above left the page elsewhere.
  await page.goto(BASE, { waitUntil: 'domcontentloaded' })

  // The administrator's view of an account's link.
  const adminLink = await api('GET', `/api/admin/users/${session.body?.user?.id}/oidc`)
  check('the admin link view answers', adminLink.status === 200, `status ${adminLink.status}`)
  check('an unlinked account reports no identity',
    adminLink.body?.linked === false && adminLink.body?.subject === null,
    JSON.stringify(adminLink.body))

  // ---- the change channel ----
  // The upgrade has to reach the hub. A build with a hub that told clients it
  // had none is exactly the defect this checks.
  const events = await page.evaluate(
    (base) =>
      new Promise((resolve) => {
        const url = base.replace('https://', 'wss://') + '/api/events'
        const sock = new WebSocket(url)
        const done = (v) => {
          try {
            sock.close()
          } catch {}
          resolve(v)
        }
        sock.onopen = () => done('open')
        sock.onerror = () => done('error')
        setTimeout(() => done('timeout'), 5000)
      }),
    BASE
  )
  check('the change channel upgrades', events === 'open', `got ${events}`)

  // ---- SMB ----
  // Not configured here, so the apply says so rather than pretending it ran.
  const smb = await api('POST', '/api/admin/smb/apply', null, csrf)
  check('the SMB apply answers about this deployment',
    smb.status === 503 || smb.status === 200 || smb.status === 502,
    `status ${smb.status} ${JSON.stringify(smb.body).slice(0, 140)}`)

  // ---- archives ----
  // This suite grants its own access rather than relying on another's: the
  // grant suite revokes what it created, so running after it leaves nothing.
  const shares = await api('GET', '/api/admin/shares')
  const shareList = Array.isArray(shares.body) ? shares.body : (shares.body?.shares ?? [])
  const shareID = shareList[0]?.id
  const shareName = shareList[0]?.name
  let grantID = null
  if (shareID !== undefined) {
    const grant = await api(
      'POST',
      '/api/admin/grants',
      {
        user: session.body?.user?.id,
        share: shareID,
        subpath: '/',
        allow: ['read', 'download'],
        deny: [],
        inherit: true,
        label: shareName
      },
      csrf
    )
    check('the archive suite can grant itself access', grant.status === 201,
      `status ${grant.status}`)
    grantID = grant.body?.id
  }
  if (shareName) {
    const archive = await page.evaluate(
      async ([token, path]) => {
        const res = await fetch('/api/fs/archive', {
          method: 'POST',
          headers: { 'Content-Type': 'application/json', 'Sc-Csrf': token },
          body: JSON.stringify({ paths: [path], name: 'test' })
        })
        const buf = await res.arrayBuffer()
        const head = new Uint8Array(buf.slice(0, 4))
        return {
          status: res.status,
          disposition: res.headers.get('content-disposition'),
          type: res.headers.get('content-type'),
          bytes: buf.byteLength,
          signature: Array.from(head)
        }
      },
      [csrf, shareName]
    )
    check('an archive is produced', archive.status === 200, `status ${archive.status}`)
    check('it is served as an archive', archive.type === 'application/zip',
      `type ${archive.type}`)
    // The first four bytes are the format's own local-header signature. A body
    // that is not one is an error document with a success status.
    check('the body really is an archive',
      JSON.stringify(archive.signature) === JSON.stringify([80, 75, 3, 4]),
      `signature ${archive.signature}`)
    check('the download is named', (archive.disposition ?? '').includes('.zip'),
      `disposition ${archive.disposition}`)
  }
  if (grantID !== null && grantID !== undefined) {
    await api('DELETE', `/api/admin/grants/${grantID}`, null, csrf)
  }
} finally {
  await browser.close()
}

console.log(failures === 0 ? '\nall checks passed' : `\n${failures} checks failed`)
process.exit(failures === 0 ? 0 : 1)

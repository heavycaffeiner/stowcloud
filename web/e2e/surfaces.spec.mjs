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
  const login = await api('POST', '/api/v1/auth/login', { login: USER, password: PASSWORD })
  check('signed in', login.status === 200, `status ${login.status}`)
  const session = await api('GET', '/api/v1/auth/session')
  const csrf = session.body?.csrf ?? ''

  // ---- recency ----
  // A bare list, not an envelope: the journal answers what this account wrote
  // and there is nothing else to carry alongside it.
  const recent = await api('GET', '/api/v1/files/recent?limit=20')
  check('recency answers', recent.status === 200, `status ${recent.status}`)
  check('recency answers with a list', Array.isArray(recent.body),
    JSON.stringify(recent.body).slice(0, 120))

  // The window is a nanosecond instant, not a date: a day count would have to
  // be resolved against somebody's clock, and the two ends of this wire are
  // frequently in different zones.
  const windowed = await api('GET', '/api/v1/files/recent?since=1600000000000000000&limit=5')
  check('recency accepts a window', windowed.status === 200, `status ${windowed.status}`)
  check('a window still answers a list', Array.isArray(windowed.body),
    JSON.stringify(windowed.body).slice(0, 120))

  // An unreadable window reads as no window rather than a refusal: a listing
  // is a read, and refusing one over a spelling takes the screen away instead
  // of showing it unfiltered.
  const badWindow = await api('GET', '/api/v1/files/recent?since=last-tuesday')
  check('an unreadable window falls back to no window', badWindow.status === 200,
    `status ${badWindow.status}`)

  // ---- the index ----
  const estimate = await api('GET', '/api/v1/admin/index/estimate')
  check('the index estimate answers', estimate.status === 200, `status ${estimate.status}`)
  // A decimal string, because a large corpus runs past what a JavaScript
  // number holds exactly and the figure an operator plans against would round.
  check('the estimate carries a size', typeof estimate.body?.index_bytes === 'string',
    JSON.stringify(estimate.body).slice(0, 160))
  check('the estimate says how much it measured',
    ['measured', 'modelled'].includes(estimate.body?.confidence),
    `confidence=${estimate.body?.confidence}`)
  check('the estimate shows its working', typeof estimate.body?.formula === 'string',
    JSON.stringify(estimate.body).slice(0, 160))

  // The build is refused outright where the index is switched off, which is
  // the default. That is a configuration state, not a fault, and it says which
  // one rather than failing anonymously.
  const build = await api('POST', '/api/v1/admin/index/build', null, csrf)
  check('the index build says whether it can run',
    build.status === 202 || build.status === 503,
    `status ${build.status} ${JSON.stringify(build.body).slice(0, 120)}`)
  if (build.status === 503) {
    check('a disabled index says so by name',
      build.body?.error?.detail?.reason_key === 'search.index_disabled',
      JSON.stringify(build.body).slice(0, 160))
  }
  if (build.status === 202) {
    check('the build answers a job id', build.body?.job !== undefined,
      JSON.stringify(build.body))
    const job = await api('GET', `/api/v1/jobs/${build.body?.job}`)
    check('the build job is readable', job.status === 200, `status ${job.status}`)
    // Underscore, which is the spelling the client switches on. It was a
    // hyphen on the wire and matched no branch there.
    check('the build job knows what kind it is', job.body?.kind === 'index_build',
      `kind=${job.body?.kind}`)
  }

  // ---- single sign-on ----
  // Reachable with no credential by necessity: the login screen asks before
  // anyone has signed in.
  const oidcConfig = await api('GET', '/api/v1/auth/oidc/config')
  check('the single-sign-on config answers', oidcConfig.status === 200,
    `status ${oidcConfig.status}`)
  check('it says whether a provider is configured',
    typeof oidcConfig.body?.enabled === 'boolean',
    JSON.stringify(oidcConfig.body))
  // The issuer and the client id are withheld from an anonymous caller.
  check('it withholds the issuer and the client id',
    oidcConfig.body?.issuer === undefined && oidcConfig.body?.client_id === undefined,
    JSON.stringify(oidcConfig.body))

  // With no provider configured the start refuses rather than redirecting:
  // there is nowhere to send the browser, and a symbolic code in a query
  // string would be a redirect to a flow that does not exist.
  const startNoProvider = await api('GET', '/api/v1/auth/oidc/start')
  check('a deployment with no provider refuses to start a flow',
    startNoProvider.status === 503,
    `status ${startNoProvider.status} ${JSON.stringify(startNoProvider.body).slice(0, 120)}`)

  // An open redirect is what this route would be if it trusted the caller's
  // destination. These are navigations, not fetches: what matters is where the
  // browser ends up, and a fetch reports an opaque redirect that proves
  // nothing about the destination.
  await page.goto(
    `${BASE}/api/v1/auth/oidc/start?return_to=${encodeURIComponent('https://evil.example.com/')}`,
    { waitUntil: 'domcontentloaded' }
  )
  check('a destination somewhere else is refused', page.url().startsWith(BASE),
    `landed on ${page.url()}`)

  await page.goto(
    `${BASE}/api/v1/auth/oidc/start?return_to=${encodeURIComponent('//evil.example.com/')}`,
    { waitUntil: 'domcontentloaded' }
  )
  check('a scheme-relative destination is refused', page.url().startsWith(BASE),
    `landed on ${page.url()}`)

  // The callback with nothing to work from stays on this server rather than
  // sending the browser somewhere a stored flow might have named.
  await page.goto(`${BASE}/api/v1/auth/oidc/callback`, { waitUntil: 'domcontentloaded' })
  check('the callback lands the browser back here', page.url().startsWith(BASE),
    `landed on ${page.url()}`)

  // Back to the app, since the navigations above left the page elsewhere.
  await page.goto(BASE, { waitUntil: 'domcontentloaded' })

  // The administrator's view of an account's link. The session carries the id
  // at the top level, not under a `user` object.
  const adminLink = await api('GET', `/api/v1/admin/users/${session.body?.id}/oidc`)
  check('the admin link view answers', adminLink.status === 200, `status ${adminLink.status}`)
  check('an unlinked account reports no identity',
    adminLink.body?.linked === false,
    JSON.stringify(adminLink.body))

  // ---- the change channel ----
  // The upgrade has to reach the hub. A build with a hub that told clients it
  // had none is exactly the defect this checks.
  const events = await page.evaluate(
    (base) =>
      new Promise((resolve) => {
        const url = base.replace('https://', 'wss://') + '/api/v1/events'
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
  const smb = await api('POST', '/api/v1/admin/smb/apply', null, csrf)
  check('the SMB apply answers about this deployment',
    smb.status === 503 || smb.status === 200 || smb.status === 502,
    `status ${smb.status} ${JSON.stringify(smb.body).slice(0, 140)}`)

  // ---- the calls that were mounted under a different verb ----
  //
  // Each of these answered "method not allowed" from a route that existed,
  // which is invisible to a check that compares paths and ignores verbs.
  const groups = await api('GET', '/api/v1/admin/groups')
  check('groups list', groups.status === 200, `status ${groups.status}`)

  const madeGroup = await api('POST', '/api/v1/admin/groups', { name: `e2e-${Date.now()}` }, csrf)
  check('a group is created', madeGroup.status === 201 || madeGroup.status === 200,
    `status ${madeGroup.status}`)
  const groupID = madeGroup.body?.id
  if (groupID !== undefined) {
    const renamed = await api('PATCH', `/api/v1/admin/groups/${groupID}`,
      { name: `e2e-renamed-${Date.now()}` }, csrf)
    check('a group is renamed', renamed.status === 200,
      `status ${renamed.status} ${JSON.stringify(renamed.body).slice(0, 120)}`)
    check('the rename answers the group', renamed.body?.name?.startsWith('e2e-renamed-') === true,
      JSON.stringify(renamed.body).slice(0, 160))
    // The response is the row the screen re-renders, so the list below is a
    // second check rather than the only one: a handler that answered success
    // and stored nothing would pass the first and fail this.
    const afterRename = await api('GET', '/api/v1/admin/groups')
    const rows = Array.isArray(afterRename.body)
      ? afterRename.body
      : (afterRename.body?.groups ?? [])
    check('the rename is visible in the list',
      rows.some((g) => String(g.id) === String(groupID) && g.name.startsWith('e2e-renamed-')),
      JSON.stringify(afterRename.body).slice(0, 160))
    const deleted = await api('DELETE', `/api/v1/admin/groups/${groupID}`, null, csrf)
    check('a group is deleted', deleted.status === 204, `status ${deleted.status}`)
  }

  // Cancelling a job. One spelling now: the client posts to the action rather
  // than deleting the job, and the DELETE alias is gone. A job nobody owns is
  // a 404, and what this asserts is that the route is mounted at all.
  const cancelled = await api('POST', '/api/v1/jobs/999999/cancel', null, csrf)
  check('cancelling a job reaches a handler rather than a method refusal',
    cancelled.status === 404,
    `status ${cancelled.status}`)

  // ---- archives ----
  const shares = await api('GET', '/api/v1/admin/shares')
  const shareList = Array.isArray(shares.body) ? shares.body : (shares.body?.shares ?? [])
  const shareID = shareList[0]?.id
  const shareName = shareList[0]?.name
  if (shareID !== undefined) {
    // No grant is created here. Registering a share grants its creator the
    // whole tree under the share's own name, and a second grant naming the
    // same subject, share, subpath and reach is refused as the duplicate it
    // is. What the archive and search checks below need is that the access
    // exists, so that is what this asserts.
    const grants = await api('GET', '/api/v1/admin/grants')
    const rows = Array.isArray(grants.body) ? grants.body : (grants.body?.grants ?? [])
    check('the archive suite already has access to the share',
      rows.some((g) => String(g.share) === String(shareID)),
      `${JSON.stringify(grants.body).slice(0, 160)}`)
  }

  // ---- the streaming search, which no route served ----
  //
  // Granted first: a search returns only what the account may read, so
  // without a grant an empty result proves nothing about whether it works.
  const search = await page.evaluate(
    (base) =>
      new Promise((resolve) => {
        const es = new EventSource(`${base}/api/v1/search/stream?q=txt`, { withCredentials: true })
        const hits = []
        const done = (v) => {
          es.close()
          resolve(v)
        }
        es.addEventListener('hit', (ev) => hits.push(JSON.parse(ev.data)))
        es.addEventListener('done', () => done({ ok: true, hits }))
        es.onerror = () => done({ ok: false, hits })
        setTimeout(() => done({ ok: false, hits, timeout: true }), 8000)
      }),
    BASE
  )
  check('the search stream opens and completes', search.ok === true,
    JSON.stringify(search).slice(0, 160))
  // The share holds two .txt files, so a search that returns none is a stream
  // that works and finds nothing, which is the failure that looks like success.
  check('the search finds what is there', search.hits?.length > 0,
    `${search.hits?.length} hits`)
  if (search.hits?.length) {
    const hit = search.hits[0]
    check('a hit carries the share and the path separately',
      typeof hit.share === 'string' && typeof hit.path === 'string',
      JSON.stringify(hit))
  }

  if (shareName) {
    const archive = await page.evaluate(
      async ([token, path]) => {
        // Two steps, and neither holds an archive. The post names the
        // selection; the get walks it into the response, which is what a
        // browser navigates to so the bytes land as they arrive.
        const prep = await fetch('/api/v1/files/archive', {
          method: 'POST',
          headers: { 'Content-Type': 'application/json', 'Sc-Csrf': token },
          body: JSON.stringify({ paths: [path], name: 'test' })
        })
        const ticket = await prep.json()
        const res = await fetch(ticket.url)
        const buf = await res.arrayBuffer()
        return {
          prepStatus: prep.status,
          hasSize: 'size' in ticket,
          status: res.status,
          disposition: res.headers.get('content-disposition'),
          type: res.headers.get('content-type'),
          length: res.headers.get('content-length'),
          bytes: buf.byteLength,
          signature: Array.from(new Uint8Array(buf.slice(0, 4)))
        }
      },
      [csrf, shareName]
    )
    check('a selection is accepted', archive.prepStatus === 200, `status ${archive.prepStatus}`)
    // Nothing is built at mint, so there is no size to report. A ticket
    // carrying one would mean the server built the archive to measure it.
    check('the ticket promises no size', archive.hasSize === false)
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
    // A stream cannot know its length before it is built, so declaring one
    // would be a number the response then contradicts.
    check('the archive declares no length', archive.length === null,
      `content-length ${archive.length}`)
  }
} finally {
  await browser.close()
}

console.log(failures === 0 ? '\nall checks passed' : `\n${failures} checks failed`)
process.exit(failures === 0 ? 0 : 1)

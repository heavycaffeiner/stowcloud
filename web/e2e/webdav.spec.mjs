// The WebDAV connection guide, in a browser via chrome-devtools-mcp.
//
// Every other spec in this tree drives Playwright directly. This one speaks
// to a real Chrome through chrome-devtools-mcp's stdio JSON-RPC surface
// instead, on purpose: the guide only proves anything if the tool that
// renders it and the tool that reads it back are different pieces of code.
//
// The MCP transport is newline-delimited JSON-RPC 2.0, not the
// Content-Length-framed variant LSP uses: chrome-devtools-mcp 1.8.0 writes
// exactly one JSON object per stdout line and nothing else. `McpClient` below
// still logs any stdout line that fails to parse as JSON, because a future
// framing change would look exactly like that: a line this parser cannot
// read, arriving on the channel this parser is listening to.
//
// chrome-devtools-mcp is launched with `pnpm dlx` rather than `npx`, per this
// project's package-manager convention; the two resolve the same package.
// Its `--acceptInsecureCerts` flag is what keeps a self-signed certificate
// from raising an interstitial, so there is nothing to dismiss and no
// fallback path to maintain.
//
// The keyboard-activation check presses a real key through the MCP tool
// rather than calling the page's copy handler from `evaluate_script`,
// because the Async Clipboard API's `writeText` only succeeds without a
// permission prompt when it runs off a trusted user gesture; CDP's
// `Input.dispatchKeyEvent` is one, a script-invoked click is not. There is no
// tool in this server's surface that grants clipboard permission directly
// (checked against its `tools/list`), so this is the only way to observe a
// real, non-mocked copy succeed.
//
// Run: node e2e/webdav.spec.mjs <base-url> <setup-token> <share-path>
import { spawn } from 'node:child_process'
import { readFileSync } from 'node:fs'

const BASE = process.argv[2] ?? 'https://127.0.0.1:18900'
const TOKEN = process.argv[3] ?? ''
// Unused by this spec (the guide has nothing to do with a share), kept as an
// argument only so every e2e.sh invocation passes the same argv shape.
const SHARE = process.argv[4] ?? ''
const USER = 'e2e-admin'
const PASSWORD = 'correct-horse-battery-staple'

// scripts/e2e.sh treats this exit code as "chrome-devtools-mcp itself never
// came up" (missing pnpm, no network to fetch the package, no browser to
// launch, a handshake that never answers) and skips this one spec instead of
// failing the run. Any other nonzero code is a real assertion failure.
const EXIT_MCP_UNAVAILABLE = 2

const MCP_PACKAGE = 'chrome-devtools-mcp@1.8.0'
const HANDSHAKE_TIMEOUT_MS = 30000
const CALL_TIMEOUT_MS = 30000
const REQUIRED_TOOLS = ['list_pages', 'navigate_page', 'take_snapshot', 'evaluate_script', 'press_key', 'list_console_messages']

let failures = 0
function check(name, ok, detail = '') {
  const mark = ok ? 'pass' : 'FAIL'
  if (!ok) failures += 1
  console.log(`  ${mark}  ${name}${detail ? `: ${detail}` : ''}`)
}

/** Thrown for anything short of "chrome-devtools-mcp answered tools/list": the
 *  spec never got far enough to test the product, as opposed to testing it
 *  and finding a defect. */
class McpUnavailable extends Error {}

/** A minimal JSON-RPC 2.0 client for the MCP stdio transport: one object per
 *  line in, one object per line out, notifications carry no id. */
class McpClient {
  constructor(proc) {
    this.proc = proc
    this.buf = ''
    this.pending = new Map()
    this.nextId = 1
    proc.stdout.on('data', (chunk) => this.#onData(chunk))
  }

  #onData(chunk) {
    this.buf += chunk.toString('utf8')
    let idx
    while ((idx = this.buf.indexOf('\n')) >= 0) {
      const line = this.buf.slice(0, idx)
      this.buf = this.buf.slice(idx + 1)
      if (!line.trim()) continue
      let msg
      try {
        msg = JSON.parse(line)
      } catch {
        console.error(`  (unexpected stdout line, not JSON-RPC: ${line.slice(0, 200)})`)
        continue
      }
      if (msg.id === undefined) continue // a server notification; none read here
      const waiter = this.pending.get(msg.id)
      if (!waiter) continue
      this.pending.delete(msg.id)
      if (msg.error) waiter.reject(new Error(`${msg.error.message} (code ${msg.error.code})`))
      else waiter.resolve(msg.result)
    }
  }

  request(method, params) {
    const id = this.nextId++
    return new Promise((resolve, reject) => {
      const timer = setTimeout(() => {
        this.pending.delete(id)
        reject(new Error(`${method} timed out after ${CALL_TIMEOUT_MS}ms`))
      }, CALL_TIMEOUT_MS)
      this.pending.set(id, {
        resolve: (v) => { clearTimeout(timer); resolve(v) },
        reject: (e) => { clearTimeout(timer); reject(e) }
      })
      this.proc.stdin.write(JSON.stringify({ jsonrpc: '2.0', id, method, params }) + '\n')
    })
  }

  notify(method, params) {
    this.proc.stdin.write(JSON.stringify({ jsonrpc: '2.0', method, params }) + '\n')
  }

  /** Calls a tool and returns its text content, joined. Throws if the tool
   *  itself reported an error, so callers never mistake a stack trace for a
   *  result. */
  async callTool(name, args) {
    const result = await this.request('tools/call', { name, arguments: args })
    const text = (result?.content ?? []).map((c) => c.text ?? '').join('\n')
    if (result?.isError) throw new Error(`tool ${name} reported an error: ${text}`)
    return text
  }
}

/** `list_pages` answers a text table, not structured JSON; the id this spec
 *  needs is the leading number of its first row. */
function parsePageId(listPagesText) {
  const m = listPagesText.match(/^(\d+):/m)
  if (!m) throw new Error(`could not find a page id in list_pages output: ${listPagesText.slice(0, 200)}`)
  return Number(m[1])
}

/** `evaluate_script` answers a fenced JSON block, except for `undefined`,
 *  which is not valid JSON and is special-cased here rather than in every
 *  caller. */
function parseEvalResult(text) {
  const m = text.match(/```json\n([\s\S]*?)\n```/)
  if (!m) throw new Error(`evaluate_script did not return the expected fenced JSON block: ${text.slice(0, 200)}`)
  const raw = m[1].trim()
  return raw === 'undefined' ? undefined : JSON.parse(raw)
}

async function evalJs(client, pageId, fn, waitForStableDom = true) {
  const text = await client.callTool('evaluate_script', { pageId, function: fn, waitForStableDom })
  return parseEvalResult(text)
}

/** The API as the browser sees it: same origin, same cookies, same guard.
 *  `evaluate_script`'s `args` only accepts element uids, not arbitrary data,
 *  so the request is built as source text instead of passed as a closure
 *  argument the way Playwright's `page.evaluate` would take it. */
async function api(client, pageId, method, path, body, csrf) {
  const hasBody = body !== undefined && body !== null
  const headerLines = []
  if (hasBody) headerLines.push("headers['Content-Type'] = 'application/json';")
  if (csrf) headerLines.push(`headers['Sc-Csrf'] = ${JSON.stringify(csrf)};`)
  const bodyExpr = hasBody ? JSON.stringify(JSON.stringify(body)) : 'undefined'
  const fn = [
    'async () => {',
    '  const headers = {};',
    ...headerLines.map((l) => '  ' + l),
    `  const res = await fetch(${JSON.stringify(path)}, { method: ${JSON.stringify(method)}, headers, body: ${bodyExpr} });`,
    '  const text = await res.text();',
    '  let parsed = null;',
    '  try { parsed = text ? JSON.parse(text) : null } catch { parsed = text }',
    '  return { status: res.status, body: parsed };',
    '}'
  ].join('\n')
  return evalJs(client, pageId, fn, false)
}

/** Mirrors session.spec.mjs's first-run and sign-in sequence: the same
 *  endpoints, the same request shapes, the same `Sc-Csrf` header name. */
async function setupAndLogIn(client, pageId) {
  const setupState = await api(client, pageId, 'GET', '/api/v1/system/setup')
  check('the setup question answers the field the client reads',
    typeof setupState.body?.required === 'boolean', JSON.stringify(setupState.body))

  if (setupState.body?.required && TOKEN) {
    const created = await api(client, pageId, 'POST', '/api/v1/system/setup', {
      token: TOKEN,
      username: USER,
      password: PASSWORD,
      app_hosts: [new URL(BASE).hostname],
      trusted_proxies: []
    })
    check('the administrator is created', created.status === 200 || created.status === 201,
      `status ${created.status} ${JSON.stringify(created.body).slice(0, 160)}`)
  }

  const login = await api(client, pageId, 'POST', '/api/v1/auth/login', { login: USER, password: PASSWORD })
  check('signing in succeeds on the path the client calls', login.status === 200,
    `status ${login.status} ${JSON.stringify(login.body)}`)

  const session = await api(client, pageId, 'GET', '/api/v1/auth/session')
  check('the session is established', session.status === 200, `status ${session.status}`)
}

async function launchMcp() {
  let proc
  try {
    proc = spawn('pnpm', ['dlx', MCP_PACKAGE, '--isolated', '--acceptInsecureCerts', '--headless'], {
      stdio: ['pipe', 'pipe', 'pipe']
    })
  } catch (e) {
    throw new McpUnavailable(`could not spawn pnpm: ${e.message}`)
  }

  const spawnError = new Promise((_, reject) => {
    proc.once('error', (e) => reject(new McpUnavailable(`pnpm failed to start: ${e.message}`)))
  })
  spawnError.catch(() => {}) // observed below only if it wins the race

  const earlyExit = new Promise((_, reject) => {
    proc.once('exit', (code, signal) => {
      reject(new McpUnavailable(
        `chrome-devtools-mcp exited before the handshake finished (code ${code}, signal ${signal})`))
    })
  })
  earlyExit.catch(() => {})

  let timeoutTimer
  const timeout = new Promise((_, reject) => {
    timeoutTimer = setTimeout(() => {
      reject(new McpUnavailable(`chrome-devtools-mcp did not complete the MCP handshake within ${HANDSHAKE_TIMEOUT_MS}ms`))
    }, HANDSHAKE_TIMEOUT_MS)
  })
  timeout.catch(() => {})

  const client = new McpClient(proc)
  const handshake = (async () => {
    await client.request('initialize', {
      protocolVersion: '2024-11-05',
      capabilities: {},
      clientInfo: { name: 'stowcloud-e2e', version: '0.0.1' }
    })
    client.notify('notifications/initialized', {})
    const list = await client.request('tools/list', {})
    return list.tools
  })()

  try {
    const tools = await Promise.race([handshake, spawnError, earlyExit, timeout])
    return { proc, client, tools }
  } catch (e) {
    proc.kill('SIGKILL')
    if (e instanceof McpUnavailable) throw e
    throw new McpUnavailable(e.message)
  } finally {
    clearTimeout(timeoutTimer)
  }
}

async function teardown(mcp) {
  if (!mcp?.proc || mcp.proc.exitCode !== null) return
  await new Promise((resolve) => {
    const timer = setTimeout(() => {
      try { mcp.proc.kill('SIGKILL') } catch { /* already gone */ }
      resolve()
    }, 3000)
    mcp.proc.once('exit', () => { clearTimeout(timer); resolve() })
    try { mcp.proc.kill('SIGTERM') } catch { clearTimeout(timer); resolve() }
  })
}

let mcp = null
try {
  console.log(`chrome-devtools-mcp: launching ${MCP_PACKAGE} via pnpm dlx`)
  mcp = await launchMcp()
} catch (e) {
  console.log(`SKIP: chrome-devtools-mcp did not come up: ${e.message}`)
  process.exit(EXIT_MCP_UNAVAILABLE)
}

try {
  const { client, tools } = mcp
  const toolNames = new Set(tools.map((t) => t.name))
  let toolSurfaceOk = true
  for (const name of REQUIRED_TOOLS) {
    const ok = toolNames.has(name)
    check(`chrome-devtools-mcp exposes the ${name} tool`, ok)
    if (!ok) toolSurfaceOk = false
  }

  if (toolSurfaceOk) {
    const pagesText = await client.callTool('list_pages', {})
    const pageId = parsePageId(pagesText)

    console.log('first run and sign-in')
    await client.callTool('navigate_page', { pageId, type: 'url', url: BASE })
    await setupAndLogIn(client, pageId)

    console.log('the webdav connection guide')
    await client.callTool('navigate_page', { pageId, type: 'url', url: `${BASE}/settings#connections` })

    const snapshot = await client.callTool('take_snapshot', { pageId })
    check('the settings page produced an accessibility snapshot', snapshot.includes('RootWebArea'),
      snapshot.slice(0, 200))

    const guidePresent = await evalJs(client, pageId,
      "() => document.querySelector('[data-testid=\"webdav-guide\"]') !== null")
    check('the webdav guide section (data-testid=webdav-guide) is present', guidePresent === true)

    const origin = await evalJs(client, pageId, '() => location.origin')
    const baseUrlText = await evalJs(client, pageId,
      "() => document.querySelector('[data-testid=\"webdav-base-url\"]')?.textContent ?? null")
    check('the base URL element (data-testid=webdav-base-url) is the page origin plus /dav',
      baseUrlText === `${origin}/dav`, `got ${JSON.stringify(baseUrlText)}, wanted ${JSON.stringify(origin + '/dav')}`)

    // Both catalogues, not just English: the app renders whichever locale the
    // session settled on, and Korean is the fallback, so asserting against
    // en.json alone failed on three of the four headings while the guide was
    // in fact naming all four. The check is that every client family is named,
    // and either translation of a heading proves that.
    const catalogue = (name) =>
      JSON.parse(readFileSync(new URL(`../src/lib/i18n/${name}.json`, import.meta.url), 'utf8'))
    const locales = { en: catalogue('en'), ko: catalogue('ko') }
    const headingKeys = ['webdav.macos_heading', 'webdav.windows_heading', 'webdav.linux_heading', 'webdav.generic_heading']
    const guideText = await evalJs(client, pageId,
      "() => document.querySelector('[data-testid=\"webdav-guide\"]')?.textContent ?? ''")
    for (const key of headingKeys) {
      const spellings = Object.values(locales).map((c) => c[key]).filter((v) => typeof v === 'string' && v.length > 0)
      check(`the guide names the client family ${key}`,
        spellings.length > 0 && spellings.some((s) => guideText.includes(s)),
        spellings.length > 0 ? `looked for any of ${JSON.stringify(spellings)}` : `no catalogue has ${key}`)
    }

    console.log('the base URL copy button, from the keyboard')
    const focused = await evalJs(client, pageId, [
      "() => {",
      "  const code = document.querySelector('[data-testid=\"webdav-base-url\"]');",
      "  const row = code ? code.closest('.sc-webdav__token-row') : null;",
      "  const button = row ? row.querySelector('button') : null;",
      "  if (button) button.focus();",
      "  return button !== null && document.activeElement === button;",
      "}"
    ].join('\n'))
    check('the base URL copy button can receive keyboard focus', focused === true)

    let announcement = ''
    if (focused) {
      await client.callTool('press_key', { pageId, key: 'Enter' })
      announcement = await evalJs(client, pageId,
        "() => document.querySelector('.sc-webdav__announce')?.textContent ?? ''")
    }
    check('activating the copy button with Enter announces the result via aria-live',
      announcement.trim().length > 0, `announcement: ${JSON.stringify(announcement)}`)

    console.log('console health on the settings page')
    const consoleText = await client.callTool('list_console_messages', { pageId })
    const errorLines = consoleText.split('\n').filter((line) => /^msgid=\d+\s+\[error\]/.test(line))
    // The same two things session.spec.mjs tracks: a script the content
    // policy blocked, and an exception nothing caught. Chrome folds both
    // into the same console channel here, distinguished by their own text
    // rather than by a separate pageerror-style event.
    const violations = errorLines.filter((line) => /Content Security Policy/i.test(line) || /\bUncaught\b/.test(line))
    check('nothing was blocked by the content policy and nothing threw uncaught',
      violations.length === 0, violations.join(' | '))
  }
} finally {
  await teardown(mcp)
}

console.log(failures === 0 ? '\nall checks passed' : `\n${failures} checks failed`)
process.exit(failures === 0 ? 0 : 1)

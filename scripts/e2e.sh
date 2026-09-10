#!/usr/bin/env bash
# The browser end-to-end run.
#
# Builds the frontend and the tagged binary, starts a server on a scratch data
# directory, and drives the shipped interface in Chromium.
#
# It exists because every other test in this tree drives a function. This one
# loads what ships and signs in the way a person does, which is the only check
# that would have caught login being mounted on the wrong path: the handler was
# correct, its tests passed, and nothing could reach it.
set -euo pipefail

cd "$(dirname "$0")/.."

if ! command -v node >/dev/null 2>&1; then
  echo "SKIP: no node" >&2
  exit 0
fi
if [ ! -d web/node_modules ]; then
  echo "SKIP: no web/node_modules; run pnpm install in web/" >&2
  exit 0
fi
# The install does not fetch a browser: Playwright downloads one separately.
# Without this check a missing browser surfaced as an uncaught exception from
# a launch deep in the suite, printing Playwright's own "just installed"
# banner and reading as a product failure.
# A launch rather than a path check: chromium.launch() resolves to the headless
# shell, which is a different download from the one executablePath() names, so
# testing that path reports missing on a machine where the suite runs.
if ! (cd web && node -e '
const { chromium } = require("playwright");
chromium.launch().then(b => b.close()).then(
  () => process.exit(0),
  () => process.exit(1),
);
' 2>/dev/null); then
  echo "SKIP: no browser; run: cd web && pnpm exec playwright install chromium" >&2
  exit 0
fi

# One bundle build per gate run. `SC_BUNDLE_FRESH=1` says the bundle in the
# tree is the current build, which is what verify.sh sets after building it
# once for this run and the embed check together.
if [ "${SC_BUNDLE_FRESH:-0}" = 1 ]; then
  echo "==> the frontend was built already"
  if [ ! -f go/engine/http/spa/build/index.html ]; then
    echo "FAIL: SC_BUNDLE_FRESH is set, but the bundle is not there" >&2
    exit 1
  fi
else
  echo "==> building the frontend"
  (cd web && pnpm build >/dev/null)
fi

echo "==> building the binary"
BIN=$(mktemp -d)/sc-engine
(cd go && CGO_ENABLED=0 GOOS=linux go build -tags embed_ui -o "$BIN" ./cmd/sc-engine)

DIR=$(mktemp -d)
mkdir -p "$DIR/data" "$DIR/share/sub"
echo hello > "$DIR/share/a.txt"
echo world > "$DIR/share/sub/b.txt"

# Everything the deployment is configured with lives in the database, so the
# settings are seeded with the one command that writes them without a server.
# The suite sends several hundred requests from one address in a few seconds,
# so the limiter is raised for the run: left at the default it refuses most of
# them, which reports as a suite failure rather than as what it is.
seed() { "$BIN" settings set "$1" --data-dir "$DIR/data" >/dev/null; }

echo '{"bind":"127.0.0.1:18900","app_hosts":["localhost"]}' | seed network
echo '{"per_sec":2000,"burst":5000}' | seed rate
echo '{"hardening":"off"}' | seed security

echo "==> serving"
"$BIN" -data "$DIR/data" > "$DIR/log" 2>&1 &
SERVER=$!
trap 'kill "$SERVER" 2>/dev/null || true' EXIT
# Ready when it answers, not after a fixed wait. Six seconds was a guess with
# no relation to what the server does, and the first spec's sign-in failing
# against a server still opening its databases reads as a product bug.
READY=0
DEADLINE=$(( SECONDS + 30 ))
while [ "$SECONDS" -lt "$DEADLINE" ]; do
  kill -0 "$SERVER" 2>/dev/null || break
  if curl -skf -o /dev/null -H "Host: localhost" https://127.0.0.1:18900/; then
    READY=1; break
  fi
  sleep 0.1
done

if ! kill -0 "$SERVER" 2>/dev/null; then
  echo "FAIL: the server exited instead of serving" >&2
  sed -n '1,40p' "$DIR/log" >&2
  exit 1
fi
if [ "$READY" -ne 1 ]; then
  echo "FAIL: the server did not answer within 30s" >&2
  sed -n '1,40p' "$DIR/log" >&2
  exit 1
fi

# From the file the server publishes it in, not the log: the token is kept out
# of the log on purpose, so scraping there silently yields an empty string and
# every check past sign-in fails as though the credential were wrong.
TOKEN=$(cat "$DIR/data/setup-token" 2>/dev/null || true)

# The browser addresses the server by a name it serves. Addressed by its
# loopback address it answers a misdirected request, which is the host guard
# working rather than a fault.
echo "==> the session, in a browser"
(cd web && node e2e/session.spec.mjs https://localhost:18900 "$TOKEN" "$DIR/share")

echo "==> the grant path, in a browser"
(cd web && node e2e/grant.spec.mjs https://localhost:18900)

echo "==> the surfaces that used to answer 501, in a browser"
(cd web && node e2e/surfaces.spec.mjs https://localhost:18900)

# chrome-devtools-mcp is a separate download from Playwright's own browser,
# fetched over the network the first time this runs, and it needs a working
# `pnpm dlx`. Either being unavailable skips this one spec rather than the
# whole run: the WebDAV guide is one screen among many, and the rest of this
# suite already proved the server itself is healthy.
if ! command -v pnpm >/dev/null 2>&1; then
  echo "SKIP: no pnpm; chrome-devtools-mcp is launched with pnpm dlx" >&2
else
  echo "==> the webdav connection guide, in a browser via chrome-devtools-mcp"
  if (cd web && node e2e/webdav.spec.mjs https://localhost:18900 "$TOKEN" "$DIR/share"); then
    :
  else
    status=$?
    if [ "$status" -eq 2 ]; then
      echo "SKIP: chrome-devtools-mcp did not come up; see the spec's own diagnostics above" >&2
    else
      exit "$status"
    fi
  fi
fi

echo "PASS: the shipped interface signs in and reaches every surface it calls"

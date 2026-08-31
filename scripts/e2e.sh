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

echo "==> building the frontend"
(cd web && pnpm build >/dev/null)

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
sleep 6

if ! kill -0 "$SERVER" 2>/dev/null; then
  echo "FAIL: the server exited instead of serving" >&2
  sed -n '1,40p' "$DIR/log" >&2
  exit 1
fi

TOKEN=$(grep -oE 'setup token \(valid[^)]*\): [a-f0-9]+' "$DIR/log" | tail -1 | awk '{print $NF}' || true)

# The browser addresses the server by a name it serves. Addressed by its
# loopback address it answers a misdirected request, which is the host guard
# working rather than a fault.
echo "==> the session, in a browser"
(cd web && node e2e/session.spec.mjs https://localhost:18900 "$TOKEN" "$DIR/share")

echo "==> the grant path, in a browser"
(cd web && node e2e/grant.spec.mjs https://localhost:18900)

echo "==> the surfaces that used to answer 501, in a browser"
(cd web && node e2e/surfaces.spec.mjs https://localhost:18900)

echo "PASS: the shipped interface signs in and reaches every surface it calls"

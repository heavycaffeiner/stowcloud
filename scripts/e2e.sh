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
  echo "SKIP: no web/node_modules; run npm ci in web/" >&2
  exit 0
fi

echo "==> building the frontend"
(cd web && npm run build >/dev/null)

echo "==> building the binary"
BIN=$(mktemp -d)/stowcloud
(cd go && CGO_ENABLED=0 GOOS=linux go build -tags embed_ui -o "$BIN" ./cmd/stowcloud)

DIR=$(mktemp -d)
mkdir -p "$DIR/data" "$DIR/share/sub"
echo hello > "$DIR/share/a.txt"
echo world > "$DIR/share/sub/b.txt"

# The suite sends several hundred requests from one address in a few seconds,
# so the limiter is raised for the run. Left at the default it refuses most of
# them, which reports as a suite failure rather than as what it is.
cat > "$DIR/sc.toml" <<TOML
[server]
data_dir = "$DIR/data"
listen = "127.0.0.1:18900"
[http]
app_hosts = ["localhost"]
content_hosts = ["content.localhost"]
[rate]
per_sec = 2000
burst = 5000
[security]
hardening = "off"

[[shares]]
name = "docs"
host_path = "$DIR/share"
TOML

echo "==> serving"
"$BIN" serve "$DIR/sc.toml" > "$DIR/log" 2>&1 &
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
(cd web && node e2e/session.spec.mjs https://localhost:18900 "$TOKEN")

echo "==> the grant path, in a browser"
(cd web && node e2e/grant.spec.mjs https://localhost:18900)

echo "PASS: the shipped interface signs in and reaches every surface it calls"

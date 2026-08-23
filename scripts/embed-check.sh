#!/usr/bin/env bash
# The embedded-build check.
#
# Build the frontend, build the binary with the embed tag, serve it, and confirm
# the bundle the browser is handed is the one just built.
#
# An earlier build system had no dependency edge to the bundle and shipped a
# stale interface once: a binary whose embedded pages predated the routes it
# served, which looked like a frontend bug for as long as it took to notice the
# bundle hash had not moved.
#
# Go's embed has a real edge, so this should be impossible. That is why it is
# checked once rather than never.
set -euo pipefail

cd "$(dirname "$0")/.."
BUNDLE_DIR=go/internal/httpapi/spa/build

echo "==> building the frontend"
(cd web && npm run build >/dev/null)

WANT=$(grep -o 'app/immutable/entry/app[A-Za-z0-9._-]*\.js' "$BUNDLE_DIR/index.html" | head -1)
if [ -z "$WANT" ]; then
  echo "FAIL: the built bundle names no entry script" >&2
  exit 1
fi
echo "    built bundle: $WANT"

echo "==> building the binary with the embed tag"
BIN=$(mktemp -d)/stowcloud
(cd go && CGO_ENABLED=0 GOOS=linux go build -tags embed_ui -o "$BIN" ./cmd/stowcloud)

# The tag is what turns the embed on. A build without it serves no interface at
# all, which is correct for a build with no bundle and would quietly pass a
# check that only looked at what was served.
#
# Counted rather than matched with an early exit: `grep -q` stops at the first
# hit, `strings` then dies of a broken pipe, and under `pipefail` a successful
# match reports failure. That is exactly what this check did on its first run,
# and it took a rebuild by hand to establish the binary had been correct all
# along.
HITS=$(strings "$BIN" | grep -c "$(basename "$WANT")" || true)
if [ "$HITS" -eq 0 ]; then
  echo "FAIL: the binary does not carry the bundle that was just built" >&2
  exit 1
fi

echo "==> serving it"
DIR=$(mktemp -d)
mkdir -p "$DIR/data"
cat > "$DIR/sc.toml" <<TOML
[server]
data_dir = "$DIR/data"
listen = "127.0.0.1:18500"
[http]
app_hosts = ["localhost"]
content_hosts = ["content.localhost"]
[security]
hardening = "off"
TOML

"$BIN" serve "$DIR/sc.toml" > "$DIR/log" 2>&1 &
SERVER=$!
trap 'kill "$SERVER" 2>/dev/null || true' EXIT
sleep 4

# A server that died rather than answered is reported as that, with its own
# output. Without this the pipeline below produces an empty result and the
# script exits on it, printing nothing: a startup panic then looks exactly like
# a bundle mismatch, which cost a debugging session.
if ! kill -0 "$SERVER" 2>/dev/null; then
  echo "FAIL: the server exited instead of serving" >&2
  sed -n '1,40p' "$DIR/log" >&2
  exit 1
fi

GOT=$(curl -sk -H "Host: localhost" https://127.0.0.1:18500/ \
      | grep -o 'app/immutable/entry/app[A-Za-z0-9._-]*\.js' | head -1 || true)
if [ -z "$GOT" ]; then
  echo "FAIL: the server served no bundle reference" >&2
  sed -n '1,40p' "$DIR/log" >&2
  exit 1
fi
echo "    served bundle: $GOT"

if [ "$WANT" != "$GOT" ]; then
  echo "FAIL: the served bundle is not the one that was built" >&2
  echo "  built:  $WANT" >&2
  echo "  served: $GOT" >&2
  exit 1
fi

# And the asset itself is reachable, not merely named in the document.
CODE=$(curl -sk -o /dev/null -w '%{http_code}' -H "Host: localhost" "https://127.0.0.1:18500/$GOT")
if [ "$CODE" != "200" ]; then
  echo "FAIL: the named bundle answered $CODE" >&2
  exit 1
fi

echo "PASS: the served bundle is the one that was built"

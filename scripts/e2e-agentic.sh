#!/usr/bin/env bash
# Stowcloud Jev-Ultrafast Agentic E2E Launcher
# Orchestrates isolated sc-engine, headless Chrome with remote debugging,
# OpenRouter Jev policy decisions, and deterministic oracles.
#
# Usage:
#   bash scripts/e2e-agentic.sh [pr|nightly]
set -euo pipefail

cd "$(dirname "$0")/.."

LANE="${1:-pr}"
if [ "$LANE" != "pr" ] && [ "$LANE" != "nightly" ]; then
  echo "Usage: $0 [pr|nightly]" >&2
  exit 1
fi

echo "========================================================"
echo "  Stowcloud Jev Agentic E2E Automation ($LANE lane)"
echo "========================================================"

# Load local .env if present
if [ -f "tools/jev-e2e/.env" ]; then
  set -a
  # shellcheck disable=SC1091
  source "tools/jev-e2e/.env"
  set +a
fi

export PYTHONPATH="tools/jev-e2e:${PYTHONPATH:-}"
if [ -f "tools/jev-e2e/.venv/bin/python3" ]; then
  PYTHON="tools/jev-e2e/.venv/bin/python3"
else
  PYTHON="python3"
fi

# 1. Run doctor preflight check
echo "==> Running doctor preflight check"
"$PYTHON" -m stowcloud_agent.runner doctor
# 2. Build binary if SC_TEST_BIN is not set
if [ -n "${SC_TEST_BIN:-}" ] && [ -f "$SC_TEST_BIN" ]; then
  BIN="$SC_TEST_BIN"
else
  echo "==> Building sc-engine binary"
  BIN=$(mktemp -d)/sc-engine
  (cd backend && CGO_ENABLED=0 go build -tags embed_ui -o "$BIN" ./cmd/sc-engine)
fi

# 3. Create isolated temporary directories
DIR=$(mktemp -d)
mkdir -p "$DIR/data" "$DIR/share/sub"
echo "hello" > "$DIR/share/a.txt"
echo "world" > "$DIR/share/sub/b.txt"
PORT=$(python3 -c 'import socket; s = socket.socket(); s.bind(("127.0.0.1", 0)); print(s.getsockname()[1]); s.close()')
seed() { "$BIN" settings set "$1" --data-dir "$DIR/data" >/dev/null; }
echo "{\"bind\":\"127.0.0.1:$PORT\",\"app_hosts\":[\"localhost\",\"127.0.0.1\"]}" | seed network
echo '{"per_sec":2000,"burst":5000}' | seed rate
echo '{"hardening":"off"}' | seed security

# 4. Start sc-engine server
echo "==> Starting isolated sc-engine on port $PORT"
"$BIN" -data "$DIR/data" > "$DIR/log" 2>&1 &
SERVER_PID=$!

cleanup() {
  echo "==> Cleaning up agentic processes and temporary state"
  kill "$SERVER_PID" 2>/dev/null || true
  if [ -n "${CHROME_PID:-}" ]; then
    pkill -P "$CHROME_PID" 2>/dev/null || true
    kill -9 "$CHROME_PID" 2>/dev/null || true
  fi
  rm -rf "$DIR" 2>/dev/null || true
  rm -rf "${CHROME_DIR:-}" 2>/dev/null || true
}
trap cleanup EXIT

# Poll health
DEADLINE=$(( SECONDS + 30 ))
READY=0
while [ "$SECONDS" -lt "$DEADLINE" ]; do
  if curl -sk -H "Host: localhost" "https://127.0.0.1:$PORT/api/v1/system/health" | grep -q '"status":"ok"'; then
    READY=1
    break
  fi
  sleep 0.2
done

if [ "$READY" -ne 1 ]; then
  echo "FATAL: sc-engine server failed to become ready on port $PORT" >&2
  cat "$DIR/log" >&2
  exit 1
fi

TOKEN=$(cat "$DIR/data/setup-token" 2>/dev/null || true)

# Create admin user
echo "==> Provisioning admin user on test instance"
curl -sk -H "Host: localhost" -H "Content-Type: application/json" -X POST \
  -d "{\"token\":\"$TOKEN\",\"username\":\"e2e-admin\",\"password\":\"Password123!\",\"app_hosts\":[\"localhost\",\"127.0.0.1\"],\"trusted_proxies\":[]}" \
  "https://127.0.0.1:$PORT/api/v1/system/setup" >/dev/null

# Create docs share
LOGIN_RESP=$(curl -sk -H "Host: localhost:$PORT" -H "Origin: https://localhost:$PORT" -H "Content-Type: application/json" -X POST \
  -c "$DIR/cookies.txt" -d '{"login":"e2e-admin","password":"Password123!"}' \
  "https://127.0.0.1:$PORT/api/v1/auth/login")
CSRF=$(echo "$LOGIN_RESP" | grep -o '"csrf":"[^"]*' | cut -d'"' -f4 || true)

SHARE_RESP=$(curl -sk -H "Host: localhost:$PORT" -H "Origin: https://localhost:$PORT" -H "Sc-Csrf: $CSRF" -b "$DIR/cookies.txt" -H "Content-Type: application/json" -X POST \
  -d "{\"name\":\"docs\",\"host\":\"$DIR/share\"}" \
  "https://127.0.0.1:$PORT/api/v1/admin/shares")
SHARE_ID=$(echo "$SHARE_RESP" | grep -o '"id":"[^"]*' | cut -d'"' -f4 || echo "1000001")

curl -sk -H "Host: localhost:$PORT" -H "Origin: https://localhost:$PORT" -H "Sc-Csrf: $CSRF" -b "$DIR/cookies.txt" -H "Content-Type: application/json" -X POST \
  -d "{\"user\":\"1\",\"share\":\"$SHARE_ID\",\"allow\":[\"read\",\"write\",\"create\",\"delete\",\"download\",\"rename\",\"move\",\"share\"],\"label\":\"docs\"}" \
  "https://127.0.0.1:$PORT/api/v1/admin/grants" >/dev/null

COOKIE_VAL=$(grep 'sc_sid' "$DIR/cookies.txt" | awk '{print $NF}' | tail -n 1 || true)

# 5. Start isolated Chrome instance with remote debugging port
CHROME_PORT=$(python3 -c 'import socket; s = socket.socket(); s.bind(("127.0.0.1", 0)); print(s.getsockname()[1]); s.close()')
CHROME_DIR=$(mktemp -d)
CHROME_BIN=$(which google-chrome || which chromium)

echo "==> Launching isolated Chrome on CDP port $CHROME_PORT"
"$CHROME_BIN" \
  --user-data-dir="$CHROME_DIR" \
  --remote-debugging-port="$CHROME_PORT" \
  --headless=new \
  --no-first-run \
  --no-default-browser-check \
  --ignore-certificate-errors \
  "about:blank" >"$DIR/chrome.log" 2>&1 &
CHROME_PID=$!

CHROME_DEADLINE=$(( SECONDS + 15 ))
CHROME_READY=0
while [ "$SECONDS" -lt "$CHROME_DEADLINE" ]; do
  if curl -fs "http://127.0.0.1:$CHROME_PORT/json/version" >/dev/null; then
    CHROME_READY=1
    break
  fi
  if ! kill -0 "$CHROME_PID" 2>/dev/null; then
    break
  fi
  sleep 0.2
done

if [ "$CHROME_READY" -ne 1 ]; then
  echo "FATAL: Chrome failed to expose CDP on port $CHROME_PORT" >&2
  cat "$DIR/chrome.log" >&2
  exit 1
fi

# 6. Run Jev agent runner
echo "==> Executing Jev autonomous agent runner"
export SC_SESSION_COOKIE="$COOKIE_VAL"
export SC_BASE_URL="https://localhost:$PORT"
export SC_SHARE_DIR="$DIR/share"
export CHROME_DEBUG_PORT="$CHROME_PORT"
"$PYTHON" -m stowcloud_agent.runner run --lane "$LANE"

echo "PASS: Stowcloud Jev agentic exploration finished in lane $LANE"

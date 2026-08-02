#!/usr/bin/env bash
# Boot the real server binary against a throwaway share and check that it
# actually serves. Run from the repo root: `bash scripts/smoke.sh`
#
# This exists because `cargo test` passed 590 times while the server, when
# actually started, logged `database is locked` within seconds and answered
# 421 to every API call from its own bind address. Neither failure is
# reachable from a unit test: one needs two live connections to the same
# SQLite file, the other needs a real Host header. Anything that only the
# assembled binary can tell us belongs here.
set -uo pipefail
cd "$(dirname "$0")/.."

PORT=${PORT:-18080}
DIR=.smoke
pass=0; fail=0
chk() { # chk <name> <expected> <actual>
  printf '%-46s' "$1"
  if [ "$2" = "$3" ]; then printf 'PASS\n'; pass=$((pass+1));
  else printf 'FAIL (want %s, got %s)\n' "$2" "$3"; fail=$((fail+1)); fi
}

rm -rf "$DIR"
mkdir -p "$DIR"/{shares/photos/sub,data,keys}
echo "hello from a real share" > "$DIR/shares/photos/note.txt"
printf 'binary\x00data' > "$DIR/shares/photos/sub/deep.bin"

W=$(cygpath -m "$PWD/$DIR" 2>/dev/null || echo "$PWD/$DIR")
cat > "$DIR/sc.toml" <<EOF
bind = "127.0.0.1:$PORT"
data_dir = "$W/data"

[[shares]]
id = 1
name = "photos"
host_path = "$W/shares/photos"
EOF

cargo build -q -p sc-server --bin sc-server || { echo "build failed"; exit 1; }
BIN=$(ls target/debug/sc-server* 2>/dev/null | grep -vE '\.(d|pdb)$' | head -1)

SC_MASTER_KEY_FILE="$W/keys/master" "$BIN" --config "$DIR/sc.toml" serve > "$DIR/serve.log" 2>&1 &
SRV=$!
trap 'kill $SRV 2>/dev/null' EXIT

for _ in $(seq 1 40); do (echo > /dev/tcp/127.0.0.1/$PORT) 2>/dev/null && break; sleep 1; done

code() { curl -s -o /dev/null -w '%{http_code}' --max-time 10 "$@"; }

echo "=== smoke: server actually runs ==="
chk "process still alive"          "alive"  "$(kill -0 $SRV 2>/dev/null && echo alive || echo dead)"
chk "no 'database is locked'"      "0"      "$(grep -ci 'database is locked' "$DIR/serve.log" || true)"
chk "no panic in log"              "0"      "$(grep -ci 'panicked' "$DIR/serve.log" || true)"
chk "GET /api/capabilities"        "200"    "$(code http://127.0.0.1:$PORT/api/capabilities)"
chk "GET /status.php (compat)"     "200"    "$(code http://127.0.0.1:$PORT/status.php)"
chk "unauth /api/fs/list -> 401"   "401"    "$(code "http://127.0.0.1:$PORT/api/fs/list?path=/photos")"
chk "unauth OPTIONS /dav -> 200"   "200"    "$(code -X OPTIONS http://127.0.0.1:$PORT/dav/)"
chk "unknown Host -> 421"          "421"    "$(code -H 'Host: evil.example.com' http://127.0.0.1:$PORT/api/capabilities)"
# 403, not 404: the token is a stateless HMAC claim, so a forged one names no
# resource whose existence a 404 could protect. `content_get` answers 403 for a
# bad signature and reserves 410 for a claim that verified but has expired or
# whose etag moved. This asserted 404 and had simply never been run.
chk "forged signed URL -> 403"     "403"    "$(code http://127.0.0.1:$PORT/c/not-a-real-token)"

echo "--- $pass passed, $fail failed ---"
[ "$fail" -eq 0 ]

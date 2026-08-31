#!/usr/bin/env bash
# A development server, reachable over the tailnet.
#
#   bash scripts/dev.sh            # start it, print where to go
#   bash scripts/dev.sh --fresh    # throw the data directory away first
#
# What it is for: looking at the shipped interface in a real browser, on a
# phone or another machine, over the tailnet rather than loopback. Everything
# it does is a development convenience and none of it is how a deployment is
# configured.
set -uo pipefail
cd "$(dirname "$0")/.."

DIR=${SC_DEV_DIR:-.dev}
PORT=${SC_DEV_PORT:-18443}

if [ "${1:-}" = "--fresh" ]; then
  echo "==> removing $DIR"
  rm -rf "$DIR"
fi

# The tailnet name, which has to be in the certificate's SANs and in the host
# allow-list. The server refuses a Host header it was not configured for, so a
# name discovered at request time would defeat the guard it is asked about.
TSNAME=$(tailscale status --json 2>/dev/null \
  | python3 -c 'import json,sys; print(json.load(sys.stdin).get("Self",{}).get("DNSName","").rstrip("."))' 2>/dev/null)
TSIP=$(tailscale ip -4 2>/dev/null | head -1)

if [ -z "$TSNAME" ]; then
  echo "no tailnet name; serving on localhost only" >&2
  TSNAME=localhost
fi

mkdir -p "$DIR"/data "$DIR"/shares/documents/notes "$DIR"/shares/pictures

# Shares with something in them, so the browse screen has rows to draw and
# there is something to delete, restore and move around.
[ -f "$DIR/shares/documents/readme.txt" ] || cat > "$DIR/shares/documents/readme.txt" <<'TXT'
A file in the development share.
TXT
[ -f "$DIR/shares/documents/notes/todo.md" ] || cat > "$DIR/shares/documents/notes/todo.md" <<'TXT'
# Notes

A second file, one directory down.
TXT
[ -f "$DIR/shares/documents/notes/deeper.txt" ] || cat > "$DIR/shares/documents/notes/deeper.txt" <<'TXT'
A third, for dragging somewhere else.
TXT
[ -f "$DIR/shares/pictures/note.txt" ] || cat > "$DIR/shares/pictures/note.txt" <<'TXT'
The second share is not empty either, so a move between shares has an end.
TXT

# A certificate the tailnet's own CA signed, when tailscale will issue one.
#
# The self-signed pair the server mints otherwise is fine in a desktop browser,
# which asks once and remembers. It is not fine on a phone: Android refuses an
# untrusted certificate for subresources outright, so the shell loads, every
# script and stylesheet fails, and what you get is a blank screen with nothing
# on it saying why. That is worse than a refusal.
#
# The server reads data/tls/cert.pem when one is there and self-signs when it
# is not, so this only has to put the file in place.
# Asked for on every run, not only when the file is missing. The server
# self-signs on first start, so after one run there is always a cert.pem there
# and a check for its absence never fires again: the message would then say the
# certificate is trusted while the self-signed one is still being served.
#
# tailscale writes the pair only when it has one to write, and it is cheap to
# ask: the daemon caches the certificate and renews it in the background.
CERT_TRUSTED=
if [ "$TSNAME" != localhost ]; then
  mkdir -p "$DIR/data/tls"
  # Announced before it runs, and bounded. Issuing the first certificate for a
  # name goes out to the CA and takes around half a minute; refusing takes as
  # long. Either way it is a silent wait, and a script that prints "removing
  # .dev" and then says nothing for thirty seconds reads as one that has hung.
  echo "==> asking tailscale for a certificate (up to 60s the first time)"
  if timeout 60 tailscale cert --cert-file "$DIR/data/tls/cert.pem" \
       --key-file "$DIR/data/tls/key.pem" "$TSNAME" >/dev/null 2>&1; then
    echo "    got one, signed by the tailnet's CA"
    CERT_TRUSTED=1
  else
    # Not fatal: the server mints its own on first start. What that costs on a
    # phone is in the note at the end.
    echo "    none; the server will self-sign"
    rm -f "$DIR/data/tls/cert.pem" "$DIR/data/tls/key.pem"
  fi
fi

echo "==> building the frontend"
if [ -d web/node_modules ]; then
  (cd web && npm run build) >/dev/null || { echo "the frontend build failed" >&2; exit 1; }
else
  echo "no web/node_modules; run npm ci in web/ for the interface" >&2
fi

echo "==> building the binary"
BIN="$PWD/$DIR/sc-engine"
(cd go && CGO_ENABLED=0 go build -tags embed_ui -o "$BIN" ./cmd/sc-engine) \
  || { echo "the build failed" >&2; exit 1; }

# Written every run: the tailnet name can change, and a stale host list is a
# server that refuses the address it is being opened at.
#
# The listener is on every interface rather than loopback, which is the whole
# point of this script. It is bounded by the tailnet: the machine's own
# firewall is what decides who reaches the port, and the host guard refuses
# anything addressed by a name that is not below.
seed() { "$BIN" settings set "$1" --data-dir "$PWD/$DIR/data" >/dev/null; }

echo "{\"bind\":\"0.0.0.0:$PORT\",\"app_hosts\":[\"$TSNAME\",\"localhost\"]}" | seed network

# Raised for a person clicking around with a hot reload in front of them. The
# shipped default is far lower and is the one a deployment gets.
echo '{"per_sec":200,"burst":500}' | seed rate

# Hardening off, because the sandbox refuses a data directory under a home
# directory on some kernels and this is a scratch tree rather than a
# deployment. A deployment leaves this alone.
echo '{"hardening":"off"}' | seed security

# A server already on the port is stopped first, and waited for. Without this
# the new one loses the bind, exits, and the checks below still passed: the
# pid file named a process that was gone while the old binary went on serving
# the old build. What that looks like from outside is a restart that changed
# nothing, which is a bad thing to have to debug through a browser.
if [ -f "$DIR/pid" ] && kill -0 "$(cat "$DIR/pid")" 2>/dev/null; then
  kill "$(cat "$DIR/pid")" 2>/dev/null
fi
for _ in $(seq 1 40); do
  ss -tln 2>/dev/null | grep -q ":$PORT " || break
  sleep 0.25
done
if ss -tln 2>/dev/null | grep -q ":$PORT "; then
  echo "something is still listening on $PORT:" >&2
  ss -tlnp 2>/dev/null | grep ":$PORT " >&2
  exit 1
fi

echo "==> serving"
# Truncated, so the readiness check below reads this run rather than matching
# a "listening" line the previous one left behind.
: > "$DIR/log"
"$BIN" -data "$PWD/$DIR/data" >> "$DIR/log" 2>&1 &
SERVER=$!
echo "$SERVER" > "$DIR/pid"

# Ready means bound, not merely alive. A process that failed to bind is alive
# for as long as it takes to print the reason and exit, which a liveness check
# hits often enough to report a dead server as a running one.
READY=
for _ in $(seq 1 60); do
  sleep 0.25
  if ! kill -0 "$SERVER" 2>/dev/null; then break; fi
  if grep -q "msg=listening" "$DIR/log" 2>/dev/null; then READY=1; break; fi
done

if [ -z "$READY" ]; then
  echo
  echo "the server did not start serving:" >&2
  sed -n '1,40p' "$DIR/log" >&2
  rm -f "$DIR/pid"
  exit 1
fi

TOKEN=$(grep -oE 'setup token \(valid[^)]*\): [a-f0-9]+' "$DIR/log" | tail -1 | awk '{print $NF}')

# Whether the account already exists, asked of the server rather than inferred
# from the token line: the token is minted at startup and stays in the log
# after it has been spent, so printing it because it is there is how somebody
# is handed a token that will answer "setup is already complete".
# Addressed by the name the server serves, resolved to loopback: the host
# guard refuses a request that arrives under an address it was not configured
# for, which is the guard working rather than a fault, and asking by IP gets
# "misdirected request" instead of an answer.
SETUP_REQUIRED=$(curl -sk --max-time 5 \
  --resolve "$TSNAME:$PORT:127.0.0.1" \
  "https://$TSNAME:$PORT/api/v1/system/setup" 2>/dev/null)

echo
echo "  https://$TSNAME:$PORT/"
[ -n "$TSIP" ] && echo "  https://$TSIP:$PORT/    (the certificate names $TSNAME, so this one warns)"
echo
case "$SETUP_REQUIRED" in
  *'"required":true'*)
    echo "  first run: the setup screen wants this token, and it expires in 15 minutes"
    echo "    $TOKEN"
    echo
    echo "  a token that has expired is a restart away: bash scripts/dev.sh"
    ;;
  *'"required":false'*)
    echo "  already set up; sign in with the account that was created"
    echo "  to start over: bash scripts/dev.sh --fresh"
    ;;
  *)
    # The server answered something else, or nothing. Saying so beats printing
    # a token that may already be spent.
    echo "  the setup state could not be read; check $DIR/log"
    ;;
esac
echo
if [ -n "$CERT_TRUSTED" ]; then
  echo "  The certificate is the tailnet CA's, so no browser warns."
else
  echo "  The certificate is self-signed: tailscale would not issue one without"
  echo "  root. A desktop browser asks once and remembers. A phone does not: it"
  echo "  refuses the subresources and draws a blank page with no error on it,"
  echo "  which is what an empty screen on a phone means here."
  echo
  echo "  For a real certificate, once:"
  echo "    sudo tailscale set --operator=\$USER"
  echo "  then: bash scripts/dev.sh --fresh"
fi
echo
echo "  log:  $DIR/log"
echo "  stop: kill \$(cat $DIR/pid)"

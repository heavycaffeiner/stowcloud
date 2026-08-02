#!/usr/bin/env bash
# Build and roll out both instances in the Rocky 9 guest.
# Run from the repo root: `bash scripts/deploy.sh`
#
# The two instances no longer deploy the same way (DEPLOYMENT.md §12.1):
#
#   production   sc-prod-docker.service -> `docker run sc:core` on :8080.
#                Deployed by rebuilding the image *in the guest* from a
#                pushed source tree, then restarting the unit. The binary
#                cross-compiled below is not what production runs.
#   development  sc-dev.service :8081, still a bare-metal systemd unit
#                running the cross-compiled musl binary this script pushes.
#
# Both are reached from Windows through .vm/tunnel.mjs. Windows only builds;
# it runs neither instance and keeps no copy of either binary afterward.
#
# `sc-prod.service` still exists in the guest, disabled — it is §12.1's
# rollback. Nothing here restarts it: `systemctl restart sc-prod` would start
# a *second* server against the same data directory while the container holds
# :8080, and the health check below would then pass against the container and
# report a deploy that never happened.
#
# The `cargo clean -p sc-http` is not optional. `#[derive(RustEmbed)]` reads
# `web/build/` during macro expansion and cargo has no dependency edge to
# those files, so rebuilding the frontend and then the binary silently
# re-embeds the *previous* bundle — has shipped a stale UI more than once,
# and it looks exactly like a frontend bug until the asset hashes are
# noticed to have never moved.
set -uo pipefail
cd "$(dirname "$0")/.."

# Git Bash (MSYS) rewrites a bare CLI argument that looks like an absolute
# POSIX path (e.g. `/opt/sc/prod/bin/...`) into a Windows path (e.g.
# `C:/Program Files/Git/opt/sc/prod/bin/...`) before node ever sees it, since
# node.exe is a native, non-MSYS program. That corrupted the remote path
# `vm.mjs put` uploads to, below, so the guest genuinely got "No such file"
# for a path that only ever existed mangled. Scoped to that one call, not
# exported for the whole script: doing that first broke curl's own `mktemp`
# paths further down (curl.exe is equally non-MSYS, but needs the *rewritten*
# Windows path to open its own `-D`/`-o` files — reproduced, exit 23).
VM="node .vm/vm.mjs"
TRIPLE=x86_64-unknown-linux-musl
BUILT="target/$TRIPLE/release/sc-server"

# The musl compiler/linker wiring, chosen by host. Sourced rather than left to
# a cargo config: the committed one hardcoded this box's absolute paths and
# broke every other host that read it.
# shellcheck source=scripts/musl-env.sh
. "$(dirname "$0")/musl-env.sh"

step() { printf '%-46s' "$1"; }
ok()   { printf 'OK\n'; }
die()  { printf 'FAIL\n'; [ -n "${1:-}" ] && echo "$1" | tail -20 | sed 's/^/      /'; exit 1; }

step "frontend build"
out=$(cd web && npm run build 2>&1) || die "$out"
# A production bundle must never carry the mock backend. client.ts throws at
# module load if it does, but by then it is already inside the binary.
if grep -rqs 'must never be embedded' web/build/ 2>/dev/null; then
  die "web/build was built with VITE_API_MOCK=1 — check web/.env*"
fi
ok

step "re-embed (cargo clean -p sc-http)"
out=$(cargo clean -p sc-http 2>&1) || die "$out"
ok

step "cross-compile (musl, release, embed-ui)"
# Uses the repo's one shared target/ (default CARGO_TARGET_DIR, same as
# every other build in this repo) — nothing here points at a private or
# temporary target directory.
out=$(cargo build -q --release --target "$TRIPLE" -p sc-server --bin sc-server --features embed-ui 2>&1) || die "$out"
[ -f "$BUILT" ] || die "expected binary not built: $BUILT"
ok

# The bundle about to ship must be the one just built, and the CSP hash the
# server will advertise for it must match the exact bytes served — checked
# locally, before anything is pushed, so a stale-embed never reaches the
# guest in the first place. (Re-checked again after restart, against the
# guest's own response, once both instances are back up.)
step "local bundle == fresh frontend build"
built_js=$(ls web/build/app/immutable/entry/ 2>/dev/null | grep '^start\.' | head -1)
[ -n "$built_js" ] || die "no start.*.js in web/build/app/immutable/entry/"
ok

# Restarting is always by systemd unit name, never by process or container
# name — a `pkill sc-server` or `docker kill` equivalent would hit whichever
# instance happens to match first, and on the old Windows deployment that
# took production down twice by mistake while meaning to touch development
# only.
restart_and_wait() {
  local unit="$1" port="$2"
  $VM run "sudo systemctl restart $unit" || die "systemctl restart $unit failed"
  for _ in $(seq 1 30); do
    code=$(curl -s -o /dev/null -w '%{http_code}' --max-time 2 "http://127.0.0.1:$port/api/health" 2>/dev/null)
    [ "$code" = "200" ] && return 0
    sleep 1
  done
  die "$unit did not come up on :$port (last health code: ${code:-none})"
}

step "push dev binary to guest (staged, atomic rename)"
# /opt/sc/dev/bin is sc-owned, so the sc user pushing over sftp can stage and
# rename in place without sudo — sudo is reserved for the things that
# actually need root, restarting units and driving docker.
# Staging under a temp name and `mv`-ing into place (same filesystem, so the
# rename is atomic) means systemd never restarts into a half-written file.
# sftp uploads with mode 0644 by default, so the executable bit is restored
# explicitly before the rename. The rename also avoids ETXTBSY: sc-dev still
# has its current binary mapped at this point, and writing through a running
# executable's inode fails outright (reproduced).
# MSYS_NO_PATHCONV scoped to just this call: the remote path is a bare
# argument starting with `/`, exactly what MSYS rewrites (see header note).
# `run`'s commands below don't need it — they arrive as one shell-quoted
# string starting with a word, not `/`, so MSYS's rewrite never matches them.
MSYS_NO_PATHCONV=1 $VM put "$BUILT" /opt/sc/dev/bin/sc-server.new >/dev/null || die "sftp push failed"
$VM run "chmod 755 /opt/sc/dev/bin/sc-server.new && mv -f /opt/sc/dev/bin/sc-server.new /opt/sc/dev/bin/sc-server" \
  || die "install on guest failed"
ok

step "restart development"
restart_and_wait sc-dev 8081
ok

# Production is the container, so it gets a source push and a `docker build`
# in the guest rather than the binary above. Deliberately the whole source
# tree and not the pre-built binary: the image builds its own frontend and
# its own musl binary in their own stages (.dockerignore excludes target/ and
# web/build/ precisely so nothing host-built can leak in), which is what
# makes the image reproducible from the repo alone.
step "push source to guest"
$VM run "rm -rf /opt/sc/build/src && mkdir -p /opt/sc/build/src" || die "could not clear build context"
# vm.mjs push exits 0 even when individual files fail — it only prints
# "FAILED n:" to stderr — so `|| die` alone would let a partial source tree
# through and `docker build` would then build whatever happened to arrive.
# Grep the output instead of trusting the exit code.
# MSYS_NO_PATHCONV for the same reason as the `put` above — the remote path
# is a bare argument starting with `/`. Without it every file is uploaded to
# `C:/Program Files/Git/opt/sc/build/src/...` and all 380 fail (reproduced).
out=$(MSYS_NO_PATHCONV=1 $VM push . /opt/sc/build/src 2>&1) || die "$out"
echo "$out" | grep -q '^FAILED' && die "$out"
ok

step "docker build sc:core (in guest)"
# --pull is omitted on purpose: every base image is digest-pinned in the
# Dockerfile, so a re-pull can only cost time, never change what is built.
out=$($VM run "cd /opt/sc/build/src && sudo docker build -t sc:core . 2>&1 | tail -40") || die "$out"
ok

step "restart production (container)"
restart_and_wait sc-prod-docker 8080
ok

# The served-bundle assertion: filename match alone missed the incident where
# the inline bootstrap script and its CSP hash came from two different
# builds (same start.*.js name, different embedded index.html) — a blank
# page with nothing louder than a CSP violation in the console. Checking
# both closes that gap.
#
# `expect_local` is the strong form, and only development can be held to it:
# dev runs the binary this script embedded the *local* web/build into, so
# anything other than a byte match means a stale embed reached the guest.
# Production builds its own frontend inside the image, under node:24-alpine
# rather than whatever node this host has, so its asset hashes are not
# required to equal the host's. It still gets the self-consistency half —
# the CSP hash the server advertises must match the document it actually
# served — which is what caught the blank-page incident.
check_bundle() {
  local url="$1" label="$2" expect_local="$3"
  local tmp_html tmp_headers fetched=""
  tmp_html=$(mktemp); tmp_headers=$(mktemp)
  # Retried like restart_and_wait: tunnel.mjs's one shared SSH connection to
  # the guest reconnects lazily on the next request after a drop (its own
  # header says so), so a request landing in that window would otherwise
  # fail this check even though the guest itself is fine.
  for _ in $(seq 1 5); do
    curl -s --max-time 10 -D "$tmp_headers" -o "$tmp_html" "$url" && { fetched=1; break; }
    sleep 1
  done
  [ -n "$fetched" ] || { rm -f "$tmp_html" "$tmp_headers"; die "$label: GET / failed after 5 retries"; }
  local served_js served_csp computed_served computed_local
  served_js=$(grep -oE 'start\.[A-Za-z0-9_-]+\.js' "$tmp_html" | head -1)
  served_csp=$(grep -i '^content-security-policy:' "$tmp_headers" | grep -oE "'sha256-[A-Za-z0-9+/=]+'" | sort)
  computed_served=$(node scripts/inline-script-hashes.mjs "$tmp_html" | tr ' ' '\n' | sort)
  computed_local=$(node scripts/inline-script-hashes.mjs web/build/index.html | tr ' ' '\n' | sort)
  rm -f "$tmp_html" "$tmp_headers"
  [ -n "$served_js" ] || die "$label: no start.*.js in the served document"
  if [ -z "$served_csp" ] || [ "$served_csp" != "$computed_served" ]; then
    die "$label: CSP hash != served document's actual inline script (server: [$served_csp] computed: [$computed_served])"
  fi
  if [ "$expect_local" = "yes" ]; then
    [ "$served_js" = "$built_js" ] || die "$label: filename mismatch: served=$served_js built=$built_js"
    [ "$computed_served" = "$computed_local" ] || die "$label: served inline script hash != local web/build/index.html (stale embed on the guest)"
  fi
}

step "dev bundle == fresh local build"
check_bundle http://127.0.0.1:8081/ dev yes
ok

step "prod bundle self-consistent (CSP == served doc)"
check_bundle http://127.0.0.1:8080/ prod no
ok

echo
echo "  production   http://127.0.0.1:8080  (sc-prod-docker.service, sc:core)"
echo "  development  http://127.0.0.1:8081  (sc-dev.service in the guest)"

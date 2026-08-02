#!/usr/bin/env bash
# The verification gate. Run from anywhere; it cd's to the repo root.
#
#   bash scripts/verify.sh              # whole workspace
#   bash scripts/verify.sh sc-core ...  # one or more crates
#
# Environment:
#   VERIFY_REQUIRE_MUSL=1   a missing musl cross toolchain is a failure, not a
#                           SKIP. CI's Linux job sets this; a fresh clone on a
#                           laptop should not have to install one to run the
#                           gate.
#   VERIFY_REQUIRE_UI=1     a missing web/build is a failure, not a SKIP. Same
#                           reasoning: both CI jobs build the frontend first,
#                           so a SKIP there means the workflow broke, not that
#                           the checkout is bare.
#
# Two rules this script exists to keep, both learned from a red CI:
#
#   1. A failing step prints everything it said. The previous version piped
#      failures through `tail -25`; when 57 tests failed at once, the `failures:`
#      name list alone overflowed that and not one panic message survived into
#      the log. Diagnosing it needed a Linux VM and half a day.
#   2. Every cargo invocation passes `--locked`, because CI and the Dockerfile
#      do. Without it this script resolves a lockfile CI will refuse, and
#      dependency drift is invisible here and fatal there.
set -uo pipefail
cd "$(dirname "$0")/.."

PKGS=()
for p in "$@"; do PKGS+=(-p "$p"); done
LABEL="${*:-workspace}"

case "$(uname -s)" in
  Linux)                HOST=linux ;;
  Darwin)               HOST=macos ;;
  MINGW*|MSYS*|CYGWIN*) HOST=windows ;;
  *)                    HOST=$(uname -s) ;;
esac

MUSL=x86_64-unknown-linux-musl

# Compiler, archiver, linker and the target-specific rustflags for the musl
# cross build, chosen by host. Everything host-specific lives there, not here
# and not in a cargo config that would apply to hosts it was never written for.
# shellcheck source=scripts/musl-env.sh
. "$(dirname "$0")/musl-env.sh"

# Can this box cross-compile to musl? Reported rather than assumed — an
# unconditional musl step on a box without the toolchain fails with a linker
# error that says nothing about the real cause.
musl_ready() {
  rustup target list --installed 2>/dev/null | grep -qx "$MUSL" || return 1
  command -v "$SC_MUSL_PROBE" >/dev/null 2>&1
}

pass=0; fail=0; skip=0
failed_names=()

# run <name> <cmd...> -- on failure, print the command and its ENTIRE output.
run() {
  local name="$1"; shift
  printf '%-60s' "$name"
  local out rc
  out=$("$@" 2>&1); rc=$?
  if [ "$rc" -eq 0 ]; then
    printf 'PASS\n'; pass=$((pass+1)); return 0
  fi
  printf 'FAIL (exit %s)\n' "$rc"
  fail=$((fail+1)); failed_names+=("$name")
  printf '\n----- %s: full output -----\n$ %s\n\n%s\n----- end %s -----\n\n' \
    "$name" "$*" "$out" "$name"
  return 1
}

# skipped <name> <why> -- or a failure, when the caller declared it required.
skipped() {
  local name="$1" why="$2" required="$3"
  if [ "$required" = 1 ]; then
    printf '%-60sFAIL\n' "$name"
    fail=$((fail+1)); failed_names+=("$name")
    printf '      required here, but: %s\n\n' "$why"
  else
    printf '%-60sSKIP (%s)\n' "$name" "$why"
    skip=$((skip+1))
  fi
}

# grep_gate <name> <hits> <hint> -- a gate whose evidence is grep output.
grep_gate() {
  local name="$1" hits="$2" hint="$3"
  printf '%-60s' "$name"
  if [ -z "$hits" ]; then printf 'PASS\n'; pass=$((pass+1)); return 0; fi
  printf 'FAIL\n'; fail=$((fail+1)); failed_names+=("$name")
  printf '\n----- %s -----\n%s\n\n%s\n----- end %s -----\n\n' "$name" "$hits" "$hint" "$name"
  return 1
}

echo "=== verifying: $LABEL ==="
echo "    host: $HOST   toolchain: $(rustc -V 2>/dev/null || echo 'rustc NOT FOUND')"
echo

# --- host build/test/lint -------------------------------------------------
# On Linux this is the only place in the pipeline where the `openat2` backend,
# the Landlock/seccomp hardening and the preview jail actually *execute*; on
# Windows it is the `portable` backend instead. Neither substitutes for the
# other, which is why CI runs this file on both.
run "cargo build ($HOST)"  cargo build --locked "${PKGS[@]}"
run "cargo test ($HOST)"   cargo test  --locked "${PKGS[@]}"
# `--all-targets` is required: it is the only way this gate sees warnings in
# test code (`#[cfg(test)]` modules, `tests/*.rs`), which a plain `cargo clippy`
# never compiles.
run "cargo clippy ($HOST, -D warnings)" \
    cargo clippy --locked --all-targets "${PKGS[@]}" -- -D warnings

# --- the deployment target ------------------------------------------------
# Host clippy never compiles a single line behind `cfg(target_os = "linux")`,
# which is exactly where the security-critical code lives. Both runs matter and
# neither subsumes the other: some lints are target-local truths — `statfs.f_type`
# is `u64` on musl and `i64` on glibc, so clippy calls the same widening cast
# redundant on one target and correct on the next.
if musl_ready; then
  run "cargo check ($MUSL)" \
      cargo check --locked --target "$MUSL" "${PKGS[@]}"
  run "cargo clippy ($MUSL, -D warnings)" \
      cargo clippy --locked --all-targets --target "$MUSL" "${PKGS[@]}" -- -D warnings
else
  why="$SC_MUSL_PROBE not on PATH, or rustup target $MUSL not installed"
  skipped "cargo check ($MUSL)"  "$why" "${VERIFY_REQUIRE_MUSL:-0}"
  skipped "cargo clippy ($MUSL)" "$why" "${VERIFY_REQUIRE_MUSL:-0}"
fi

# --- compat-layer isolation (DESIGN-COMPAT.md §1.3) --------------------
# Scans *code*, not comments. A core crate may explain in a comment why a
# protocol-neutral abstraction exists (e.g. why sc-upload has a name-ordered
# spool mode); what it may not do is embed compat wire vocabulary in
# behaviour — header names, route strings, error text.
CORE_DIRS=$(ls -d crates/sc-{vfs,meta,core,acl,auth,dav,upload,http,watch,search,preview,smb}/src 2>/dev/null)
NC_HITS=""
if [ -n "$CORE_DIRS" ]; then
  NC_HITS=$(grep -rIn -iE '\boc[:_-]|\bocs\b|remote\.php' $CORE_DIRS 2>/dev/null \
            | grep -vE '^[^:]+:[0-9]+:\s*(//|/\*|\*)')
fi
grep_gate "compat isolation: no oc:/ocs/remote.php in core code" "$NC_HITS" \
  "Compat wire vocabulary belongs in sc-compat-nc, behind the ports traits."

# --- no Korean in server code ---------------------------------------------
# The server never decides what language a reader wants: a refusal travels as a
# catalogue key plus its placeholders and the browser renders it
# (`web/src/lib/api/error-text.ts`). Korean reaching a wire field, a comment or
# a log line is the shape that gate exists to remove — the settings screen used
# to print `detail.reason` raw, in Korean, whatever locale you had picked.
# The frontend has `web/tools/i18n-check.mjs`; this is its counterpart, and the
# hole it closes is that the server had none.
#
# The allowlist is CJK *test data*, not copy: Hangul folding and trigram
# fixtures, a compat path fixture, and config/OIDC display-name fixtures.
KO_ALLOW='sc-search/src/(fold|matcher|trigram|index/(base|mod))\.rs|sc-compat-nc/src/dav_paths\.rs|sc-http/src/(content|archive_zip|routes)\.rs|sc-server/src/(config|oidc)\.rs'
KO_HITS=$(grep -rIn -P '[\x{AC00}-\x{D7A3}]' crates/*/src 2>/dev/null | grep -vE "$KO_ALLOW")
grep_gate "no Korean in crates/*/src" "$KO_HITS" \
  "Send a catalogue key, not a sentence; keep comments in English."

# --- the bind site must install ConnectInfo -------------------------------
# `axum::serve(listener, router)` compiles, runs, serves every request happily,
# and silently denies the process any knowledge of where requests come from: no
# peer means no trusted proxy, which means CF-Connecting-IP is thrown away too,
# and the login brute-force gate, the API rate limiter and the audit log's IP
# column all collapse onto one address for the entire internet.
#
# A grep because the failure is invisible to the test suite: every test builds
# its own service, so a test can prove `connect_info_service` works but not that
# `cmd_serve` calls it. Reverting the call site leaves the suite green — checked,
# not assumed.
BARE_SERVE=$(grep -rIn 'axum::serve(' crates/sc-server/src 2>/dev/null | grep -v 'connect_info_service')
grep_gate "bind site installs ConnectInfo" "$BARE_SERVE" \
  "serve through sc_server::connect_info_service(router)"

# --- the feature-strip build must drop NC entirely ------------------------
if [ -d crates/sc-server ]; then
  run "build --no-default-features (NC stripped)" \
      cargo build --locked -p sc-server --no-default-features
fi

# --- embed-ui: the real single-binary build -------------------------------
# `#[derive(RustEmbed)]` reads `web/build/` during macro expansion, so these
# features are off by default and this section needs a built frontend
# (`cd web && npm run build`).
#
# `cargo clean -p sc-http` first, and this is not paranoia: cargo has no
# dependency edge to those files, so re-running `npm run build` and then `cargo
# build` reuses the *previously embedded* frontend, silently. That shipped a
# stale UI once — a binary whose embedded SPA predated the routes it served,
# which looked like a frontend bug for as long as it took to notice the bundle
# hash had not moved.
if [ -f web/build/index.html ]; then
  cargo clean -p sc-http 2>/dev/null
  run "cargo build -p sc-http --features embed-ui" \
      cargo build --locked -p sc-http --features embed-ui
  run "cargo clippy -p sc-http --features embed-ui (-D warnings)" \
      cargo clippy --locked -p sc-http --all-targets --features embed-ui -- -D warnings
  run "cargo build -p sc-server --features embed-ui" \
      cargo build --locked -p sc-server --features embed-ui
  if musl_ready; then
    run "cargo check -p sc-server --features embed-ui ($MUSL)" \
        cargo check --locked --target "$MUSL" -p sc-server --features embed-ui
  else
    skipped "cargo check -p sc-server --features embed-ui ($MUSL)" \
            "no musl cross toolchain" "${VERIFY_REQUIRE_MUSL:-0}"
  fi
else
  why="no web/build; run: cd web && npm run build"
  for s in "cargo build -p sc-http --features embed-ui" \
           "cargo clippy -p sc-http --features embed-ui (-D warnings)" \
           "cargo build -p sc-server --features embed-ui" \
           "cargo check -p sc-server --features embed-ui ($MUSL)"; do
    skipped "$s" "$why" "${VERIFY_REQUIRE_UI:-0}"
  done
fi

# --- what this run did NOT verify -----------------------------------------
# Everything above ran against the working tree; CI and the Dockerfile build
# HEAD. Commit b705bfd staged a caller (`sc-server/src/diagnostics.rs` calling
# `MetaStore::writes_blocked`) but not the implementation, which sat unstaged;
# every build from then on failed at HEAD with E0599 while this script stayed
# green, and the hunt went to musl, LTO and runner disk before the diff.
DIRTY=$(git status --porcelain 2>/dev/null)
if [ -n "$DIRTY" ]; then
  echo
  echo "--- NOTE: this ran on the working tree, and it is not HEAD ---"
  echo "$DIRTY" | sed 's/^/      /'
  echo "      A green run above says nothing about what CI builds."
fi

echo
if [ "$fail" -ne 0 ]; then
  echo "--- failed steps ---"
  for n in "${failed_names[@]}"; do echo "      $n"; done
fi
echo "--- $pass passed, $fail failed, $skip skipped ---"
[ "$fail" -eq 0 ]

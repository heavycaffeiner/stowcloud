#!/usr/bin/env bash
# The verification gate. Run from anywhere; it cd's to the repo root.
#
#   bash scripts/verify.sh
#
# Environment:
#   VERIFY_REQUIRE_UI=1     a missing frontend build is a failure, not a SKIP.
#                           CI builds the frontend first, so a SKIP there means
#                           the workflow broke rather than that the checkout is
#                           bare.
#   VERIFY_REQUIRE_GOTOOLS=1
#                           a golangci-lint or govulncheck that could not be
#                           found or installed is a failure, not a SKIP.
#   VERIFY_REQUIRE_RACE=1   a skipped race run is a failure. The detector needs
#                           cgo, which the Windows box has no compiler for, so
#                           it runs where one exists and CI is where that is.
#
# One rule this script exists to keep, learned from a red CI: a failing step
# prints everything it said. An earlier version piped failures through a tail;
# when dozens of tests failed at once the name list alone overflowed it and not
# one message survived into the log, and diagnosing that took a day.
set -uo pipefail
cd "$(dirname "$0")/.."

LABEL="the workspace"

case "$(uname -s)" in
  Linux)                HOST=linux ;;
  Darwin)               HOST=macos ;;
  MINGW*|MSYS*|CYGWIN*) HOST=windows ;;
  *)                    HOST=$(uname -s) ;;
esac

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
echo "    host: $HOST   go: $(go version 2>/dev/null || echo 'go NOT FOUND')"

echo

# ==========================================================================
# The Go half.
#
# A failing step prints everything it said, and a step that did not run says so
# with a name the caller can require.
#
# Everything here builds and vets for GOOS=linux, because that is the only
# target that ships, and tests for the host's own OS, because a linux test
# binary does not execute on the development box. Cross-compiled test binaries
# for the packages that need a real kernel are copied to the Linux guest and
# run there; that loop is deliberate and is what a portable filesystem backend
# would have hidden two real bugs behind.
# ==========================================================================

GO_TOOLS="$PWD/go/.tools/bin"
# Pinned, because a linter that changes its rule set between two runs of this
# script is a gate that means something different each time.
GOLANGCI="github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.12.2"
# Not pinned, and deliberately: its whole job is to know about advisories
# published after this line was written.
GOVULN="golang.org/x/vuln/cmd/govulncheck@latest"

# native <path> -- a path in the form this host's own programs understand.
native() { if [ "$HOST" = windows ]; then cygpath -w "$1"; else printf '%s' "$1"; fi; }

# ingo <args...>      -- the shipping build environment, run from go/.
# ingo_host <args...> -- this host's own OS, which is what tests need.
# ingo_cgo <args...>  -- the one environment that is not the shipping one.
#
# CGO_ENABLED=0 is written out on every step rather than left to the default,
# and that is load-bearing rather than tidy: go defaults it to 1 whenever a C
# compiler is on PATH, so on a box that has one the difference between a static
# binary and one linked against libc is this word.
ingo()      { ( cd go && env CGO_ENABLED=0 GOOS=linux "$@" ); }
ingo_host() { ( cd go && env CGO_ENABLED=0 "$@" ); }

# ingo_cgo <args...> -- the one environment that is not the shipping one.
#
# On Windows the compiler has to be on a PATH the *go command* can read, and
# this shell's is not one: go is a native program and cannot resolve an MSYS
# path. Finding a compiler here and not handing its directory over is how this
# step failed with `C compiler "gcc" not found` on a box that has one.
ingo_cgo() {
  if [ "$HOST" = windows ]; then
    ( cd go && env CGO_ENABLED=1 \
        PATH="$(native "$(dirname "$(cc_path)")");$(native "$(dirname "$(command -v go)")")" \
        "$@" )
  else
    ( cd go && env CGO_ENABLED=1 "$@" )
  fi
}

# cc_path -- the C compiler cgo can drive, or nothing. Probed, never
# configured: a committed absolute path to one box's toolchain is what the musl
# wiring had to be taken apart for.
cc_path() {
  command -v cc 2>/dev/null || command -v gcc 2>/dev/null || command -v clang 2>/dev/null
}

have_cc() { [ -n "$(cc_path)" ]; }

# go_tool <name> <module@version> -- print the path to a gate tool, installing
# it under go/.tools/bin if it is not already there or on PATH. Installing into
# the checkout rather than GOPATH/bin means a gate run writes nothing outside
# the repository it was pointed at.
go_tool() {
  local name="$1" mod="$2" p
  for p in "$GO_TOOLS/$name" "$GO_TOOLS/$name.exe"; do
    [ -x "$p" ] && { printf '%s' "$p"; return 0; }
  done
  p=$(command -v "$name" 2>/dev/null)
  if [ -n "$p" ]; then printf '%s' "$p"; return 0; fi
  ( cd go && env CGO_ENABLED=0 GOBIN="$(native "$GO_TOOLS")" go install "$mod" ) \
    >/dev/null 2>&1 || return 1
  for p in "$GO_TOOLS/$name" "$GO_TOOLS/$name.exe"; do
    [ -x "$p" ] && { printf '%s' "$p"; return 0; }
  done
  return 1
}

# native_tool <exe> <args...> -- run a program that is not an MSYS one. On
# Windows it cannot read this shell's PATH at all, and golangci-lint shells out
# to the go command, so it is handed a PATH holding exactly that.
native_tool() {
  local exe="$1"; shift
  if [ "$HOST" = windows ]; then
    ( cd go && env CGO_ENABLED=0 GOOS=linux \
        PATH="$(native "$(dirname "$(command -v go)")")" "$exe" "$@" )
  else
    ( cd go && env CGO_ENABLED=0 GOOS=linux "$exe" "$@" )
  fi
}

if [ -f go/go.mod ] && command -v go >/dev/null 2>&1; then
  echo
  echo "=== go: $(go version) ==="
  echo

  # Both architectures the image publishes. arm64 is not a formality: the Rust
  # tree ships an arm64 image whose process seccomp filter is an empty list,
  # and nothing in its gate compiled that path.
  run "go build (linux/amd64)" ingo env GOARCH=amd64 go build ./...
  run "go build (linux/arm64)" ingo env GOARCH=arm64 go build ./...
  run "go vet (linux)"         ingo go vet ./...

  # The two in-tree analysers and the text scan. They run for the host's own
  # OS because `go run` has to execute what it built, and each one is pointed
  # at the shipping target from the inside.
  run "vetgo (D7: one goroutine spawn)"        ingo_host go run ./tools/vetgo ./cmd ./internal
  # The client and the route table are two halves of one contract, and nothing
  # else here checks that they agree. A route the frontend calls and the server
  # does not mount is a screen that cannot work, and it was invisible to every
  # other check in this tree: that is how login ended up mounted on the
  # change-password path with the whole suite green.
  #
  # The whole client directory and the verb, not one file and the path alone.
  # Pointed at one file it missed the streaming search, which no route served;
  # comparing paths alone it missed six calls mounted under a different verb,
  # each of which answers "method not allowed" from a route that exists.
  run "routecheck (the client's paths are mounted)" \
      ingo_host go run ./tools/routecheck \
        -client-dir ../web/src/lib/api \
        -routes internal/server/routes.go \
        -allow routes.allow
  run "vetsecret (D12: no secret to a verb)"   ingo_host go run ./tools/vetsecret ./...
  run "koscan (D15: no Korean in Go source)"   ingo_host go run ./tools/koscan ./cmd ./internal ./tools

  FMT=$(cd go && gofmt -l . 2>/dev/null)
  grep_gate "gofmt" "$FMT" "Run: cd go && gofmt -w ."

  if LINT=$(go_tool golangci-lint "$GOLANGCI"); then
    run "golangci-lint run" native_tool "$LINT" run ./...
  else
    skipped "golangci-lint run" "not on PATH and could not be installed" \
            "${VERIFY_REQUIRE_GOTOOLS:-0}"
  fi

  run "go test ($HOST)" ingo_host go test -count=1 ./...

  # D17. The detector needs cgo and cgo needs a C compiler, so this is the one
  # step whose environment differs from the shipping build's. The binary it
  # produces is a test binary and is never shipped.
  #
  # The condition is "is there a compiler", not "is this Linux". Those looked
  # like the same question while this box had no compiler on it, and they are
  # not: a race the detector can find is a race in portable code, and the host
  # that runs the tests every day is worth finding it on.
  if have_cc; then
    run "go test -race ($HOST)" ingo_cgo go test -race -count=1 ./...
  else
    skipped "go test -race" "no C compiler on PATH, and the detector needs cgo" \
            "${VERIFY_REQUIRE_RACE:-0}"
  fi

  # D16. The gate runs the committed seed corpus; the nightly job runs the
  # fuzzer.
  #
  # The pattern is '^Fuzz' and not 'Fuzz.*/corpus'. Go names a seed entry after
  # the file it came from, or "seed#N" for one added in code, so the second
  # pattern selects no subtest at all and the step passed while running nothing
  # -- the same shape as a gate that reports PASS whatever the tree contains.
  run "fuzz seed corpus" ingo_host go test -run '^Fuzz' -count=1 ./...

  if VULN=$(go_tool govulncheck "$GOVULN"); then
    run "govulncheck" native_tool "$VULN" ./...
  else
    skipped "govulncheck" "not on PATH and could not be installed" \
            "${VERIFY_REQUIRE_GOTOOLS:-0}"
  fi

  # D18. The module graph against the checked-in allowlist, so a new direct
  # dependency is a diff to a file rather than a line in go.mod nobody reads.
  DEPS_WANT=$(grep -vE '^[[:space:]]*(#|$)' go/deps.allow | sort)
  DEPS_HAVE=$(ingo go list -m -f '{{if and (not .Main) (not .Indirect)}}{{.Path}}{{end}}' all \
              2>/dev/null | grep -v '^$' | sort)
  DEPS_DIFF=$(diff <(printf '%s\n' "$DEPS_WANT") <(printf '%s\n' "$DEPS_HAVE") 2>/dev/null)
  grep_gate "direct modules match go/deps.allow" "$DEPS_DIFF" \
    "< is allowed and absent, > is present and not allowed. Edit go/deps.allow."

  # D1. Exceptions are countable, and the count is committed, so one being
  # added shows up in the diff beside the reason it was added for.
  NOLINT_WANT=$(grep -vE '^[[:space:]]*(#|$)' go/nolint.budget | head -1 | tr -d '[:space:]')
  NOLINT_HAVE=$(grep -rIo '//nolint:[a-zA-Z,]*' go --include='*.go' 2>/dev/null | wc -l | tr -d '[:space:]')
  NOLINT_HITS=""
  [ "$NOLINT_WANT" = "$NOLINT_HAVE" ] || \
    NOLINT_HITS="go/nolint.budget says $NOLINT_WANT, the tree has $NOLINT_HAVE"
  grep_gate "//nolint count matches go/nolint.budget" "$NOLINT_HITS" \
    "Every exception carries a reason on its line. Update the budget deliberately."

  # Three rules that are about a call appearing outside the one package that
  # owns it. Each scans code, not comments, for the same reason the compat
  # gate below does: a package may explain in a comment why the exception
  # exists without being the exception.
  go_code() { grep -rIn --include='*.go' -E "$1" go 2>/dev/null | grep -vE '^[^:]+:[0-9]+:[[:space:]]*(//|\*)'; }

  # D8. One clock. F10 was a now_ns that unwrapped duration_since(UNIX_EPOCH),
  # which aborts the process on a machine whose RTC has not been set.
  CLOCK_HITS=$(go_code 'time\.Now\(' | grep -v '^go/internal/clock/')
  grep_gate "D8: time.Now only in internal/clock" "$CLOCK_HITS" \
    "Take a clock.Clock. Nothing else reads the wall clock."

  # D9. crypto/rand only.
  RAND_HITS=$(go_code '"math/rand(/v2)?"')
  grep_gate "D9: no math/rand" "$RAND_HITS" \
    "crypto/rand. A predictable token is a token."

  # D11. A raw rename is callable from internal/vfs and nowhere else. That is
  # all this proves: the package holds four operations with different contracts
  # -- WriteDurable for staged share content, ShareRoot.Rename for a namespace
  # move, PublishNew for an already-complete database with no clobber, and
  # ReplaceFileDurable for a trusted private control file -- and no grep can
  # tell which one a caller should have taken.
  RENAME_HITS=$(go_code 'os\.Rename\(|unix\.Renameat2?\(' | grep -v '^go/internal/vfs/')
  grep_gate "D11: rename only from internal/vfs" "$RENAME_HITS" \
    "Take the operation in internal/vfs whose contract matches, and add one there if none does."

  # Every descriptor is an *os.File and every use of a raw one keeps the file
  # alive across the call. (*os.File).Fd takes the descriptor out of the
  # runtime's view for the duration, so nothing keeps the owner reachable and a
  # finalizer is free to close it underneath the syscall. Two helpers do the
  # keepalive; this is what stops a third site doing it by hand and forgetting.
  FD_HITS=$(go_code '\.Fd\(\)' \
            | grep -vE '^go/internal/(vfs/open\.go|jail/landlock\.go):')
  grep_gate "raw descriptors only through a keepalive helper" "$FD_HITS" \
    "Use withFd or withFd2 in internal/vfs; internal/jail/landlock.go is the other named site."

  # F5. IntentReadWrite has exactly one call site, the upload engine's lazy
  # reopen of a part file. A second one is a read path holding a writable
  # descriptor again, which is the defect the argument replaced.
  # internal/vfs is where it is declared and dispatched on, so the count is of
  # what is outside it. Test code is excluded: a test that proves the two
  # intents differ has to name both.
  RW_SITES=$(go_code 'IntentReadWrite' | grep -v '_test\.go:' \
             | grep -vc '^go/internal/vfs/')
  RW_HITS=""
  [ "${RW_SITES:-0}" -le 1 ] || RW_HITS=$(go_code 'IntentReadWrite' \
    | grep -v '_test\.go:' | grep -v '^go/internal/vfs/')
  grep_gate "IntentReadWrite has at most one call site" "$RW_HITS" \
    "Only the upload finalizer may take a writable descriptor on a read path."

  # D14. SQL is parameters only. Every statement is a package-level constant.
  SQL_HITS=$(go_code 'fmt\.Sprintf\(|fmt\.Sprint\(|strings\.Builder' \
             | grep '^go/internal/store/' || true)
  grep_gate "D14: no built SQL in internal/store" "$SQL_HITS" \
    "Bind parameters. A query built from parts is an injection waiting for input."

  # D19. Closes F8, where two files carried thirteen per cent of the Rust tree
  # with no seam a reader could navigate by.
  BIG=$(find go -name '*.go' -not -path '*/testdata/*' \
        -exec awk 'END { if (NR > 1500) printf "%s: %d lines\n", FILENAME, NR }' {} \; 2>/dev/null)
  grep_gate "no Go file over 1,500 lines" "$BIG" \
    "Split along a seam the problem already has, not one invented to hit a count."

  # Principle 4, by the import graph rather than by a text search. The grep it
  # replaces reads source, so a constant defined elsewhere or a string built
  # from parts passes it while doing exactly what it exists to prevent.
  go_compat_isolation() {
    local hits="" core=""
    for d in core dav auth acl store upload vfs preview search httpapi watch smb; do
      [ -d "go/internal/$d" ] && core="$core ./internal/$d/..."
    done
    # G1: no core package imports the compat layer, transitively.
    if [ -n "$core" ]; then
      # shellcheck disable=SC2086
      hits="$hits$(ingo go list -deps $core 2>/dev/null | grep 'internal/compat' || true)"
    fi
    # G2: the layer imports the seam and nothing else from the tree.
    hits="$hits$(ingo go list -f '{{range .Imports}}{{.}}{{"\n"}}{{end}}' ./internal/compat/nc/... \
                 2>/dev/null | grep 'stowcloud/go/internal/' | grep -v 'internal/compat/ncport$' || true)"
    # G3: the seam carries no vendor vocabulary.
    hits="$hits$(grep -rIn -iE '\boc[:_-]|\bocs\b|remote\.php|nextcloud' \
                 go/internal/compat/ncport/ 2>/dev/null || true)"
    # G5: the text scan, kept and narrowed, because it catches a core package
    # that learned the vocabulary without importing anything.
    for d in core dav auth acl store upload vfs preview search httpapi watch smb; do
      [ -d "go/internal/$d" ] || continue
      hits="$hits$(grep -rIn -iE '\boc[:_-]|\bocs\b|remote\.php' "go/internal/$d" 2>/dev/null || true)"
    done
    printf '%s' "$hits"
  }
  if [ -d go/internal/compat ]; then
    NC_HITS=$(go_compat_isolation)
    grep_gate "compat isolation (import graph, seam, text)" "$NC_HITS" \
      "Compat wire vocabulary belongs behind internal/compat/ncport."
    # G4: the stripped build, which is stronger than the feature flag it
    # replaces: with no tag the packages are not compiled at all.
    run "go build (compat stripped)" ingo go build ./...
    run "go build -tags compat_nc"   ingo go build -tags compat_nc ./...
    # The layer's own tests, which the untagged run above cannot see: with no
    # tag those files are not compiled at all, so a build that only checks the
    # stripped tree checks none of this phase's behaviour.
    run "go test -tags compat_nc"    ingo_host go test -tags compat_nc ./internal/compat/...
    run "fuzz seed corpus (compat)"  ingo_host go test -tags compat_nc -run '^Fuzz' -count=1 ./internal/compat/...
  else
    skipped "compat isolation (import graph, seam, text)" \
            "go/internal/compat does not exist yet" "${VERIFY_REQUIRE_COMPAT:-0}"
  fi

  # The single-binary build. `//go:embed` reads the bundle with a real
  # dependency edge: rebuilding after `npm run build` picks up the new files or
  # fails to compile, so there is no stale-bundle hazard to clean around. The bundle lives inside the embedding package because //go:embed
  # cannot name a path outside it, and refuses a symlink that points out.
  if [ -f go/internal/httpapi/spa/build/index.html ]; then
    run "go build -tags embed_ui" ingo go build -tags embed_ui ./...
    # And the served bundle is the one that was built, which is the property
    # the dependency edge exists for. It needs node, so it only runs where the
    # frontend can be rebuilt.
    if command -v npm >/dev/null 2>&1; then
      run "the embedded bundle is the built one" bash scripts/embed-check.sh
      # A real browser against the shipped interface. It is the only check here
      # that asks whether a request arrives rather than whether a function is
      # correct, which is the distinction that let login sit on the wrong path
      # with the whole suite green.
      run "the interface signs in and reaches its surfaces" bash scripts/e2e.sh
    else
      skipped "the embedded bundle is the built one" "no npm" "${VERIFY_REQUIRE_UI:-0}"
    fi
  else
    skipped "go build -tags embed_ui" "no built frontend; run: cd web && npm run build" \
            "${VERIFY_REQUIRE_UI:-0}"
  fi
else
  why="no go/go.mod, or the go toolchain is not on PATH"
  for s in "go build (linux/amd64)" "go vet (linux)" "golangci-lint run" "go test ($HOST)"; do
    skipped "$s" "$why" 0
  done
fi

# --- the SMB sidecar agent -------------------------------------------------
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

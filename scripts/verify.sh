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
GOLANGCI="github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.13.1"
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
# The go command's own directory is in that PATH because the replacement is
# total: `env PATH=...` on Windows leaves the child unable to find go itself,
# which reported as `env: 'go': No such file or directory` rather than as
# anything about cgo. The tool being run is resolved by this shell, which still
# has its own PATH, so it is invoked by the absolute path it resolved to.
ingo_cgo() {
  if [ "$HOST" = windows ]; then
    local go_exe; go_exe=$(command -v "$1") || return 127
    shift
    ( cd go && env CGO_ENABLED=1 \
        PATH="$(native "$(dirname "$(cc_path)")");$(native "$(dirname "$(command -v go)")");$PATH" \
        "$go_exe" "$@" )
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
#
# A cached binary is reused only when it was built by the toolchain now on
# PATH. Both of these tools load and type-check source with the go/* packages
# they were compiled against, so one built by an older release cannot parse a
# newer standard library: golangci-lint panicked on go1.27's math/rand/v2 and
# govulncheck refused to load any package at all. Both reported as a gate
# failure against this tree, and neither had anything to do with this tree.
go_tool() {
  local name="$1" mod="$2" p
  local want
  want=$(go env GOVERSION 2>/dev/null)
  for p in "$GO_TOOLS/$name" "$GO_TOOLS/$name.exe"; do
    [ -x "$p" ] || continue
    if [ -z "$want" ] || go version "$p" 2>/dev/null | grep -qF "$want"; then
      printf '%s' "$p"; return 0
    fi
    # Built by a different toolchain. Removed rather than reported, because a
    # stale one here is not a thing anybody chose.
    rm -f "$p"
  done
  p=$(command -v "$name" 2>/dev/null)
  if [ -n "$p" ] && { [ -z "$want" ] || go version "$p" 2>/dev/null | grep -qF "$want"; }; then
    printf '%s' "$p"; return 0
  fi
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
    # cd through a native path, not this shell's. A native program reads the
    # working directory it is given, and handing it /d/a/... left govulncheck
    # reporting "no go.mod file" from a directory that has one.
    # The go directory is prepended to the existing PATH rather than replacing
    # it. A total replacement left golangci-lint unable to run `go env`, since
    # it shells out and inherits this: it reported "executable file not found
    # in %PATH%" for go on a machine where go had just run.
    ( cd "$(native "$PWD/go")" && env CGO_ENABLED=0 GOOS=linux \
        PATH="$(native "$(dirname "$(command -v go)")");$PATH" "$exe" "$@" )
  else
    ( cd go && env CGO_ENABLED=0 GOOS=linux "$exe" "$@" )
  fi
}

if [ -f go/go.mod ] && command -v go >/dev/null 2>&1; then
  echo
  echo "=== go: $(go version) ==="
  echo

  # Both architectures the image publishes. arm64 is not a formality: an
  # earlier build shipped an arm64 image whose process seccomp filter was an
  # empty list, and nothing in its gate compiled that path.
  run "go build (linux/amd64)" ingo env GOARCH=amd64 go build ./...
  run "go build (linux/arm64)" ingo env GOARCH=arm64 go build ./...
  run "go vet (linux)"         ingo go vet ./...

  # The gate builds two architectures and one OS, so a package that stops
  # compiling off Linux reaches CI instead of this script. It happened: files
  # naming Linux-only types were added to packages without the build tag their
  # neighbours carry, and the Windows job found it after the push.
  #
  # Nothing here ships for Windows. What is checked is that the tags are
  # consistent, which is a property of this tree rather than of that target.
  # Packages excluded in full are the expected outcome, not a failure, so only
  # a real type error fails this.
  # vet, package by package, rather than a build of ./...: a build resolves
  # the whole import graph first and stops at the command, which imports
  # packages that are excluded here in full, so it never reaches the package
  # that does not compile. vet type-checks each one on its own.
  #
  # Matched on the shape of a compiler error, because the excluded-package
  # paragraphs are the expected outcome and are not failures.
  # One package at a time, because a single unresolvable import anywhere in
  # the set aborts the whole run before any package is type-checked, and the
  # command imports packages that are excluded here in full. Vetting each on
  # its own means an excluded neighbour costs one skipped package rather than
  # the entire check.
  # Both tag sets, because the compat build is a separate set of files and
  # broke the same way independently.
  #
  # A test build rather than a vet: what the Windows job runs is go test, and
  # tagging a package Linux-only without tagging what imports it turns a clean
  # vet into a "setup failed" there. Compiling the test binaries is what
  # surfaces that.
  #
  # -o /dev/null, not -run with a pattern that matches nothing: this host is
  # Linux and the binaries are Windows ones, so running them is an exec format
  # error for every package. They are built and thrown away.
  run "the build tags hold off Linux" bash -c '
    cd go
    fail=0
    for tags in "" "-tags compat_nc"; do
      # "?" lines name a package with no tests, and "go: downloading" is the
      # module cache filling on a clean checkout. Both go to stderr and
      # neither is a failure.
      out=$(CGO_ENABLED=0 GOOS=windows go test $tags -o /dev/null -c ./... 2>&1 |
              grep -vE "^(\?[[:space:]]|go: downloading )")
      if [ -n "$out" ]; then printf "%s\n" "$out"; fail=1; fi
    done
    exit $fail'

  # The two in-tree analysers and the text scan. They run for the host's own
  # OS because `go run` has to execute what it built, and each one is pointed
  # at the shipping target from the inside.
  run "vetgo (D7: one goroutine spawn)"        ingo_host go run ./tools/vetgo ./cmd ./engine
  # The client and the route table are two halves of one contract, and nothing
  # else here checks that they agree. A route the frontend calls and the server
  # does not mount is a screen that cannot work, and it was invisible to every
  # other check in this tree: that is how login ended up mounted on the
  # change-password path with the whole suite green.
  #
  # The whole client tree and the verb, not one file and the path alone.
  # Pointed at one file it missed the streaming search, which no route served;
  # comparing paths alone it missed six calls mounted under a different verb,
  # each of which answers "method not allowed" from a route that exists.
  #
  # src rather than src/lib/api: the resumable upload transport is a sibling
  # directory, so a narrower path saw none of its calls, and the screens under
  # routes/ build URLs of their own that no .ts file names.
  #
  # Aimed at the engine's table, which is what the client now calls: every
  # path it sends carries the /api/v1 prefix. Pointing this at the old table
  # would pass only by comparing the client against a surface it no longer
  # talks to.
  #
  # Two files, because two things mount routes: the versioned table, and the
  # public link surface, whose five paths carry no version and are registered
  # in code. A check that read only the table reported all five as screens
  # that cannot work, when they answer.
  run "routecheck (the client's paths are mounted)" \
      ingo_host go run ./tools/routecheck \
        -client-dir ../web/src \
        -routes engine/http/server/v1table.go,engine/lifecycle/publiclink.go \
        -allow routes.allow \
        -server-only routes.server-only
  # routecheck proves the paths exist. This proves the bodies match: the
  # client read fields the server never sent, and every one of them was found
  # by a person clicking something that then did nothing.
  run "contractcheck (the client's fields are sent)" \
      ingo_host go run ./tools/contractcheck \
        ../web/src/lib/api/types.ts ./engine/http/handler ./engine/lifecycle
  # contractcheck compares response shapes. This compares the settings surface,
  # where the drift runs the other way: the section handler stores the client's
  # JSON object unchanged, on purpose, so a field name only the client knows is
  # saved happily and read by nobody. The save reports success and the control
  # never does anything.
  #
  # Found by writing "concurrent" where the loader reads "max_concurrent_fast"
  # and watching the value store and vanish.
  run "settingscheck (a stored setting is read)" \
      ingo_host go run ./tools/settingscheck \
        ../web/src/lib/api/types.ts ./engine/service/settings/runtimecfg/load.go
  # freshscan keeps the engine's comments from being the old tree's. This keeps
  # the phase documents from describing a tree that moved on: each numbered
  # deliberate change names what it is about, and a rename leaves the prose
  # describing something that is not there.
  #
  # Every built phase: 0 (foundation), 1 (core) and 2. The audit documents
  # describe the old tree on purpose, and phase 3's describe what is not built
  # yet, so both would report their own subject matter as missing.
  #
  # Foundation and core were left out when this gate was written and were the
  # only areas nothing checked. Adding them brought 175 change entries under
  # the gate and found six documents naming the old tree's spelling of a
  # symbol; each was checked by hand and is recorded in the tool's ignore list
  # with what replaced it.
  #
  # http joined once its packages existed. It found a defect in the tool: an
  # identifier with a slash was assumed to be a file path, so a route path was
  # reported missing while sitting in the route table. Four of its six findings
  # were that bug.
  run "speccheck (the phase documents match the engine)" bash -c '
    cd go
    fail=0
    for area in foundation core auth oidc upload search preview settings smb http; do
      if ! go run ./tools/speccheck "../docs/refactor/$area" ./engine; then fail=1; fi
    done
    exit $fail'
  run "vetsecret (D12: no secret to a verb)"   ingo_host go run ./tools/vetsecret ./...
  run "koscan (D15: no Korean in Go source)"   ingo_host go run ./tools/koscan ./cmd ./tools ./engine
  # The tier rule over the rebuilt engine, by the import graph. A package's
  # tier is its first path element under engine/, and an import is legal only
  # when the importing tier lists the imported one.
  run "layercheck (the engine's tiers hold)" ingo_host go run ./tools/layercheck ./engine
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
  #
  # Two counts while the two trees coexist. The old tree's is frozen and may
  # only go down; the engine's starts at zero, so an exception there is a diff
  # to the second number rather than a rounding error inside the first.
  nolint_count() {
    grep -rIno '//nolint:[a-zA-Z,]*' go --include='*.go' 2>/dev/null \
      | grep -c "$@" | tr -d '[:space:]'
  }
  NOLINT_WANT=$(grep -vE '^[[:space:]]*(#|$)' go/nolint.budget | sed -n 1p | tr -d '[:space:]')
  NOLINT_WANT_ENGINE=$(grep -vE '^[[:space:]]*(#|$)' go/nolint.budget | sed -n 2p | tr -d '[:space:]')
  NOLINT_HAVE=$(nolint_count -v '^go/engine/')
  NOLINT_HAVE_ENGINE=$(nolint_count '^go/engine/')
  NOLINT_HITS=""
  [ "$NOLINT_WANT" = "$NOLINT_HAVE" ] || \
    NOLINT_HITS="the old tree: go/nolint.budget says $NOLINT_WANT, it has $NOLINT_HAVE"
  [ "$NOLINT_WANT_ENGINE" = "$NOLINT_HAVE_ENGINE" ] || \
    NOLINT_HITS="$NOLINT_HITS"$'\n'"the engine: go/nolint.budget says $NOLINT_WANT_ENGINE, it has $NOLINT_HAVE_ENGINE"
  NOLINT_HITS=$(printf '%s' "$NOLINT_HITS" | sed '/^$/d')
  grep_gate "//nolint counts match go/nolint.budget" "$NOLINT_HITS" \
    "Every exception carries a reason on its line. Update the budget deliberately."

  # Three rules that are about a call appearing outside the one package that
  # owns it. Each scans code, not comments, for the same reason the compat
  # gate below does: a package may explain in a comment why the exception
  # exists without being the exception.
  go_code() { grep -rIn --include='*.go' -E "$1" go 2>/dev/null | grep -vE '^[^:]+:[0-9]+:[[:space:]]*(//|\*)'; }

  # D8. One clock. F10 was a now_ns that unwrapped duration_since(UNIX_EPOCH),
  # which aborts the process on a machine whose RTC has not been set.
  # One per tree while the two coexist; the old entry goes with the old tree.
  CLOCK_HITS=$(go_code 'time\.Now\(' \
               | grep -vE '^go/(internal/clock|engine/kit/clock)/')
  grep_gate "D8: time.Now only in the clock packages" "$CLOCK_HITS" \
    "Take a clock.Clock. Nothing else reads the wall clock."

  # D9. crypto/rand only.
  RAND_HITS=$(go_code '"math/rand(/v2)?"')
  grep_gate "D9: no math/rand" "$RAND_HITS" \
    "crypto/rand. A predictable token is a token."

  # D11. A raw rename is callable from the packages that own a rename contract
  # and nowhere else. That is all this proves: each holds operations with
  # different contracts (WriteDurable for staged share content, ShareRoot.Rename
  # for a namespace move, ReplaceFileDurable for a trusted private control
  # file) and no grep can tell which one a caller should have taken.
  #
  # The engine splits the two: share content renames live in engine/infra/vfs,
  # control-file renames in engine/store/fsatomic, which is the move that took
  # control-file writing out of the filesystem-security package.
  RENAME_HITS=$(go_code 'os\.Rename\(|unix\.Renameat2?\(' \
                | grep -vE '^go/(internal/vfs|engine/infra/vfs|engine/store/fsatomic)/')
  grep_gate "D11: rename only from the packages that own it" "$RENAME_HITS" \
    "Take the operation whose contract matches: vfs for share content, fsatomic for a control file."

  # Every descriptor is an *os.File and every use of a raw one keeps the file
  # alive across the call. (*os.File).Fd takes the descriptor out of the
  # runtime's view for the duration, so nothing keeps the owner reachable and a
  # finalizer is free to close it underneath the syscall. A helper per owning
  # package does the keepalive; this is what stops another site doing it by
  # hand and forgetting.
  #
  # The engine's list is longer than the old tree's because the rebuild split
  # ownership: vfs and fsatomic each hold a withFd helper for the files they
  # open, preview owns one for the socket it passes descriptors over, and jail
  # calls the three landlock syscalls directly since each takes a descriptor
  # alongside a packed struct no wrapper covers.
  FD_HITS=$(go_code '\.Fd\(\)' \
            | grep -vE '^go/(internal/(vfs/open\.go|jail/landlock\.go)|engine/infra/vfs/root\.go|engine/infra/jail/landlock\.go|engine/store/fsatomic/dir_linux\.go|engine/service/preview/transport\.go):')
  grep_gate "raw descriptors only through a keepalive helper" "$FD_HITS" \
    "Use the withFd helpers where the descriptor's owner lives; every other site goes through them."

  # F5. IntentReadWrite has exactly one call site, the upload engine's lazy
  # reopen of a part file. A second one is a read path holding a writable
  # descriptor again, which is the defect the argument replaced.
  # internal/vfs is where it is declared and dispatched on, so the count is of
  # what is outside it. Test code is excluded: a test that proves the two
  # intents differ has to name both.
  #
  # One upload engine, so one site is the rule and two is the defect. A
  # comment naming the intent is not a call site, so the count is of the
  # calls.
  rw_sites() { go_code 'OpenRead\([^)]*IntentReadWrite' | grep -v '_test\.go:' | grep -E "$1"; }
  RW_FOUND=$(rw_sites '^go/engine/')
  RW_HITS=""
  [ "$(printf '%s' "$RW_FOUND" | grep -c .)" -le 1 ] || RW_HITS="$RW_FOUND"
  grep_gate "IntentReadWrite has at most one call site" "$RW_HITS" \
    "Only the upload finalizer may take a writable descriptor on a read path."

  # D14. SQL is parameters only. Every statement is a package-level constant.
  # Tests are excluded: they build fixture strings rather than statements, and
  # a seeded row named with Sprintf is not a query.
  SQL_HITS=$(go_code 'fmt\.Sprintf\(|fmt\.Sprint\(|strings\.Builder' \
             | grep '^go/engine/store/' | grep -v '_test\.go:' || true)
  grep_gate "D14: no built SQL in the store" "$SQL_HITS" \
    "Bind parameters. A query built from parts is an injection waiting for input."

  # D19. Closes F8, where two files carried thirteen per cent of the tree with
  # no seam a reader could navigate by.
  BIG=$(find go -name '*.go' -not -path '*/testdata/*' \
        -exec awk 'END { if (NR > 1500) printf "%s: %d lines\n", FILENAME, NR }' {} \; 2>/dev/null)
  grep_gate "no Go file over 1,500 lines" "$BIG" \
    "Split along a seam the problem already has, not one invented to hit a count."

  # Principle 4, by the import graph rather than by a text search. The grep it
  # replaces reads source, so a constant defined elsewhere or a string built
  # from parts passes it while doing exactly what it exists to prevent.
  go_compat_isolation() {
    # G6: the same two rules over the rebuilt engine. Vendor vocabulary lives
    # only under engine/http/compat, and that package imports no service,
    # store or infra type: its ports are declared for assembly to implement.
    #
    # Code only, not comments. A package may name the compatibility layer to
    # explain why a boundary exists without being on the wrong side of it, and
    # the middleware's own tests declare a protocol path as fixture data.
    engine_vendor() {
      grep -rIn --include='*.go' -iE '\bocs\b|remote\.php|nextcloud' "$1" 2>/dev/null \
        | grep -vE '^[^:]+:[0-9]+:[[:space:]]*(//|\*)' || true
    }
    # Initialised before the loops append to it: under `set -u` an unset name
    # aborts the function, which reported this gate as passing without it
    # having looked at anything.
    hits=""
    if [ -d go/engine ]; then
      for d in kit store service infra; do
        [ -d "go/engine/$d" ] || continue
        hits="$hits$(engine_vendor "go/engine/$d")"
      done
      for d in apierr archive dav emergency handler route; do
        [ -d "go/engine/http/$d" ] || continue
        hits="$hits$(engine_vendor "go/engine/http/$d")"
      done
      # server names the prefixes because it is what mounts them. The
      # interface fallback has to know which paths belong to another protocol
      # so it never answers an HTML page where a sync client expected a
      # multistatus, and that list is the reservation. Only the fallback and
      # its declaration may say so; a vendor name anywhere else under server
      # is the assembly learning a protocol it should be handed.
      if [ -d go/engine/http/server ]; then
        hits="$hits$(engine_vendor go/engine/http/server \
                     | grep -vE '^go/engine/http/server/(fallback|preflight)' || true)"
      fi
      # middleware declares protocol paths as data supplied by whoever mounts
      # a protocol, so its own fixtures name one. Only its non-test files are
      # scanned: a shipped path there would be the layer knowing a vendor.
      if [ -d go/engine/http/middleware ]; then
        hits="$hits$(grep -rIn --include='*.go' -iE '\bocs\b|remote\.php|nextcloud' \
                     go/engine/http/middleware 2>/dev/null \
                     | grep -v '_test\.go:' \
                     | grep -vE '^[^:]+:[0-9]+:[[:space:]]*(//|\*)' || true)"
      fi
      if [ -d go/engine/http/compat ]; then
        hits="$hits$(ingo go list -tags compat_nc -f '{{range .Imports}}{{.}}{{"\n"}}{{end}}' \
                     ./engine/http/compat/... 2>/dev/null \
                     | grep 'stowcloud/go/engine/' \
                     | grep -vE 'engine/http/(dav|apierr)$|engine/kit/' || true)"
      fi
    fi
    # Blank lines only means no hit. Several of the scans above end with a
    # newline whether or not they matched, and a whitespace-only string is not
    # empty to the caller's test.
    printf '%s' "$hits" | grep -v '^[[:space:]]*$' || true
  }
  if [ -d go/engine/http/compat ]; then
    NC_HITS=$(go_compat_isolation)
    grep_gate "compat isolation (import graph, seam, text)" "$NC_HITS" \
      "Compat wire vocabulary belongs behind the compat layer."
    # G4: the stripped build, which is stronger than the feature flag it
    # replaces: with no tag the packages are not compiled at all.
    run "go build (compat stripped)" ingo go build ./...
    run "go build -tags compat_nc"   ingo go build -tags compat_nc ./...
    # The layer's own tests, which the untagged run above cannot see: with no
    # tag those files are not compiled at all, so a build that only checks the
    # stripped tree checks none of this phase's behaviour.
    #
    # Both packages, because the vocabulary and the mount are tested in
    # different places: `http/compat` holds the wire format and
    # `lifecycle` holds the routes, the DAV aliases and the client flows that
    # exercise them. Naming only the first left every mounted route untested,
    # which is how an Engine assembled without a clock reached a released
    # handler. The repeated untagged cases cost about a minute; a shipped
    # surface with no gate costs more.
    #
    # Only where the host is the shipping target. This layer is Linux-only,
    # like everything it wraps, so off Linux the pattern matches no packages
    # and go reports that as an error: a gate step failing because the code it
    # names does not exist on this OS says nothing about the code.
    if [ "$HOST" = linux ]; then
      run "go test -tags compat_nc"    ingo_host go test -tags compat_nc ./engine/http/compat/... ./engine/lifecycle/...
      run "fuzz seed corpus (compat)"  ingo_host go test -tags compat_nc -run '^Fuzz' -count=1 ./engine/...
    else
      skipped "go test -tags compat_nc" "the compat layer is Linux only" 0
      skipped "fuzz seed corpus (compat)" "the compat layer is Linux only" 0
    fi
  else
    skipped "compat isolation (import graph, seam, text)" \
            "go/engine/http/compat does not exist yet" "${VERIFY_REQUIRE_COMPAT:-0}"
  fi

  # The single-binary build. `//go:embed` reads the bundle with a real
  # dependency edge: rebuilding the frontend picks up the new files or fails
  # to compile, so there is no stale-bundle hazard to clean around. The bundle
  # lives inside the embedding package because //go:embed cannot name a path
  # outside it, and refuses a symlink that points out.
  if [ -f go/engine/http/spa/build/index.html ]; then
    run "go build -tags embed_ui" ingo go build -tags embed_ui ./...
    # And the served bundle is the one that was built, which is the property
    # the dependency edge exists for. It needs node, so it only runs where the
    # frontend can be rebuilt.
    if command -v pnpm >/dev/null 2>&1; then
      run "the embedded bundle is the built one" bash scripts/embed-check.sh
      # A real browser against the shipped interface. It is the only check here
      # that asks whether a request arrives rather than whether a function is
      # correct, which is the distinction that let login sit on the wrong path
      # with the whole suite green.
      run "the interface signs in and reaches its surfaces" bash scripts/e2e.sh
    else
      skipped "the embedded bundle is the built one" "no pnpm" "${VERIFY_REQUIRE_UI:-0}"
    fi
  else
    skipped "go build -tags embed_ui" "no built frontend; run: cd web && pnpm build" \
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
# HEAD. A commit once staged a caller but not the implementation it called,
# which sat unstaged; every build from then on failed at HEAD while this script
# stayed green, and the hunt went to the toolchain and the runner disk before
# anybody read the diff.
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

# Gate and toolchain - Spec Proposal

| Item       | Detail                           |
|------------|----------------------------------|
| Author     | heavycaffeiner(Dong Hyun Kim)    |
| Created    | 2026-08-12                       |
| Status     | **Draft**                        |
| Reviewers  |                                  |

---

## 1. Summary

Phase 0. The module layout, the build, the lint set, the Go half of
`scripts/verify.sh`, and the four assumptions this proposal makes that a
compile settles in minutes. Nothing functional ships in this phase; the gate is
the deliverable, because every rule in
[`stowcloud-1-defensive-standard.md`](stowcloud-1-defensive-standard.md) is
worth what enforces it and nothing more.

## 2. Background & Motivation

`scripts/verify.sh` is what decides whether a change is releasable, and it
carries two rules learned from a red CI: a failing step prints everything it
said, and every build passes `--locked`. Both survive.

A third rule is inherited from
[`stowcloud-0`](stowcloud-0-motivation-and-findings.md) §2.5 S3, and it is the
one a gate is most likely to lose: **a step that did not run says so, loudly,
and the caller decides whether that is acceptable.** The Rust gate has this in
`VERIFY_REQUIRE_MUSL` and `VERIFY_REQUIRE_UI`: a missing toolchain is a SKIP on
a laptop and a failure in CI, because a SKIP there means the workflow broke
rather than that the checkout is bare. The Go gate keeps the mechanism even
though it needs it for fewer steps, because the failure it prevents is a green
run that verified less than the reader assumed. What does not survive is
the shape underneath them, which is six Rust steps and two greps, four of which
exist to work around problems Go does not have:

- the musl cross-compile probe, and the SKIP path when the toolchain is absent;
- `cargo clean -p sc-http` before the embed build, because cargo has no
  dependency edge to `web/build`;
- the separate host and musl clippy runs, because host clippy never compiles a
  line behind `cfg(target_os = "linux")`;
- the `--no-default-features` build, which becomes a build tag.

The Go replacement has fewer steps and covers more, and the reason is not that
Go is better at this: it is that one of these four is a cross-compilation
problem, one is a build-system gap, one is a conditional-compilation gap, and Go
has different versions of all three.

## 3. Goals & Non-Goals

### 3.1 Goals

- [ ] `go/` builds, vets and tests with one command on the Windows box and in
      the Linux VM, with no toolchain beyond Go itself.
- [ ] Every tier-2 rule from document 1 enforced before any code it governs
      exists.
- [ ] The two custom `go vet` analysers (D7, D12) written and wired.
- [ ] `verify.sh` gains a Go half and keeps the Rust half until cutover, with
      one summary line for both.
- [ ] The four assumptions in §4.4 settled by compiling, and the answers written
      back into the documents that depend on them.

### 3.2 Non-Goals

- [ ] Any behaviour. No handler, no schema, no syscall. A `main` that prints its
      version is the whole of `cmd/`.
- [ ] Removing the Rust half of the gate. It goes at Phase 13.
- [ ] A `GOOS=windows` build of anything that touches the filesystem. §4.4
      states what that costs.
- [ ] Reproducible-build work beyond `-trimpath` and `-buildvcs`. Worth doing,
      not worth blocking on.

## 4. Technical Design

### 4.1 Architecture Overview

```
go/
  go.mod                module github.com/heavycaffeiner/stowcloud/go
  go.sum
  cmd/
    stowcloud/          the only binary. Subcommands: serve, healthcheck,
                        preview-worker, migrate, smb-render
  internal/
    limits/ clock/ task/ secret/ num/ apierr/    the D-rule packages
    vfs/ jail/ store/ acl/ auth/ core/ watch/
    search/ upload/ preview/ httpapi/ dav/ smb/ server/
    compat/nc/          behind the compat_nc build tag
  tools/
    vetgo/              D7: the go-statement analyser
    vetsecret/          D12: the Secret formatting analyser
  testdata/
    golden/             cross-implementation fixtures (search, ETag, wire)
```

**One binary, four entry points.** `cmd/stowcloud` dispatches on `argv[1]` the
way `crates/sc-server/src/main.rs` already intercepts `healthcheck` before the
flag parser runs. This is contradiction C2 from the folder README resolved: the
overview proposed a second binary for the preview worker, which doubles the
image for no gain when the process can exec its own path with a different
argument. The `Dockerfile` comment that anticipates a second binary becomes
obsolete rather than satisfied.

**Why `internal/`.** Everything below it is unimportable from outside the
module. That is the compiler enforcing what `publish = false` plus a comment
enforced in `Cargo.toml`.

### 4.2 Data Model Changes

None.

### 4.3 Core Logic

#### 4.3.1 The build

```sh
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
  go build -trimpath -tags 'compat_nc,embed_ui' \
           -ldflags '-s -w -X main.version=...' \
           -o out/stowcloud ./cmd/stowcloud
```

- `CGO_ENABLED=0` is not an optimisation, it is the whole static-binary story
  and the reason there is no C toolchain in the builder image. It is also what
  [`stowcloud-5-store-and-schema.md`](stowcloud-5-store-and-schema.md) is
  constrained by.
- `-trimpath` removes the build machine's paths from the binary, which the
  current Rust build gets from `strip = true` only partially.
- `embed_ui` is a build tag rather than a default because a fresh checkout has
  no `web/build`, which is the same reason the Rust feature is off by default.
  The difference is that `//go:embed` creates a real dependency edge, so the
  `cargo clean -p sc-http` hazard has no counterpart: rebuilding after
  `npm run build` picks up the new files or fails to compile.

#### 4.3.2 The gate

`scripts/verify.sh` gains a Go section, keeping the existing `run`, `skipped`
and `grep_gate` helpers so failure output behaves identically:

| Step | Command | Replaces |
|---|---|---|
| build | `go build ./...` | `cargo build` |
| build, compat stripped | `go build -tags '' ./...` | `cargo build --no-default-features` |
| vet | `go vet ./...` plus the two in-tree analysers | part of clippy |
| lint | `golangci-lint run` | `cargo clippy -D warnings` |
| test | `go test -race -count=1 ./...` | `cargo test` |
| fuzz corpus | `go test -run 'Fuzz.*/corpus' ./...` | nothing today |
| vuln | `govulncheck ./...` | `cargo-deny advisories` |
| deps | direct-module allowlist diff | `cargo-deny bans` |
| import graph | `go list -deps` compat check | the `oc:`/`ocs` grep |
| text scan | the narrowed vocabulary and Korean scans | the two greps |
| file size | no file over 1,500 lines | nothing today |
| embed | `go build -tags embed_ui` when `web/build` exists | the embed-ui steps |

Two steps in the Rust half have no Go counterpart and are deleted with it: the
musl cross probe (Go cross-compiles with no toolchain, so the SKIP path has
nothing to skip) and the second clippy run under the musl target (`go vet` on
`GOOS=linux` compiles every `_linux.go` file, and that is the only target that
ships).

One step from the Rust half is kept verbatim in spirit: the working-tree
warning. `git status --porcelain` at the end, with the note that a green run on
a dirty tree says nothing about HEAD. That one cost half a day once and the
reason is unchanged.

#### 4.3.3 Cross-compilation and the development host

`GOOS=linux GOARCH=amd64 go build ./...` runs on the Windows box with nothing
installed but Go. So does `go vet`. What does not run there is `go test` for any
package that calls a Linux syscall, because the package does not compile for
`GOOS=windows` at all.

That is deliberate, and it is contradiction C3 resolved. The alternative is a
portable backend, and the Rust tree's own comments record what having one cost:

> `open_dir`, not `resolve_dir`: the latter hands back an `O_PATH` descriptor,
> and `fsync` on `O_PATH` is EBADF [...] so on Linux every write returned
> `Io(EBADF)`; Windows compiles `portable` instead and could not see it.

and

> `sc-upload` creates the part file here, then verifies its digest through the
> cached handle at finalize; on Linux that read failed unconditionally. Windows
> compiles `portable`, which has always opened `read(true).write(true)`.

Two bugs that broke every write and every upload finalize on the only platform
that ships, both invisible on the development host precisely because a second
implementation was standing in for the real one. A backend that exists so tests
can run somewhere they do not have to be correct is a backend that hides the
bugs it was built to route around.

**What this costs, stated rather than glossed.** `go test ./...` on Windows runs
the packages that do not touch the filesystem: `limits`, `clock`, `task`,
`secret`, `num`, `apierr`, `dav`'s XML scanner, `search`'s codec and ranking,
`auth`'s hashing, `httpapi`'s middleware order, the config parser. It does not
run `vfs`, `jail`, `preview`, `watch`, `store`, or any integration test. Those
need the Linux VM, and the VM becomes a required part of the loop rather than a
convenience.

**What makes that tolerable** is that it is one command against a running guest,
and that the guest needs no Go toolchain: the Windows box cross-compiles the
test binaries (`go test -c -o`) and copies them over. This is worth confirming
at Phase 0 rather than assuming, and it is assumption A4 in §4.4.

#### 4.3.4 The image

`Dockerfile` loses the Rust builder stage, `musl-tools`, `pkg-config`,
`cargo install cargo-auditable`, the `rust_target.txt` dance and the four
`CC_*`/`AR_*` environment variables. It keeps the frontend stage unchanged, the
distroless runtime, the digest pinning discipline with its verification notes,
the `HEALTHCHECK` exec form, and both licence copies.

The SBOM story changes shape rather than disappearing: `cargo auditable`'s
`.rust-audit-info` section is replaced by Go's own embedded build info, which
`go version -m /stowcloud` reads. It is a default rather than a wrapper tool, so
the pinned `cargo install` line and the version-skew note against
`supply-chain.yml` both go away, and the workflow reads the binary instead.

### 4.4 The four assumptions, settled by compiling

Each of these is something this document and the ones after it depend on and
this machine cannot confirm. Phase 0's real output is the answers.

**A1. The syscall surface is present and shaped as assumed.** A single file that
references `unix.Openat2`, `unix.OpenHow`, `unix.Statx`, `unix.Renameat2`,
`unix.CopyFileRange`, `unix.CloseRange`, `unix.Fstatfs`, `unix.Getdents`,
`unix.ParseDirent`, `unix.Socketpair`, `unix.UnixRights`,
`unix.ParseSocketControlMessage`, `unix.Prctl`, `unix.Setrlimit` and
`unix.Exec`, compiled for both `GOARCH=amd64` and `arm64`. If any is absent or
differently shaped, it becomes a raw `unix.Syscall` and the document that uses
it is corrected.

**A2. Landlock has wrappers, or it does not.** `unix.LandlockCreateRuleset`,
`unix.LandlockAddRule`, `unix.LandlockRestrictSelf`. If absent, the jail
package calls `unix.Syscall` directly, which is what its seccomp half does
regardless, so this changes an implementation detail and no design.

**A3. The image decoders cover what the current build accepts.** `image/jpeg`,
`image/png`, `image/gif` are standard library; `x/image/bmp`, `x/image/tiff`,
`x/image/webp` are not, and WebP is the one worth checking, because the Rust
build's `image` crate accepts both lossy and lossless WebP. If `x/image/webp`
does not, [`stowcloud-12-preview.md`](stowcloud-12-preview.md) records reduced
WebP support explicitly rather than letting a format quietly stop working.

**A4. Test binaries cross-compile and run in the guest.** `go test -c` for
`GOOS=linux`, copied to the Rocky guest, executed there. If the loop is
unworkable, §4.3.3's trade has to be reopened, and it is better reopened in
Phase 0 than in Phase 5.

None of the four blocks the phase; all four block a document that depends on
them, and each one's answer is written back into that document.

## 5. API Design

### 5-1. New / Modified

```go
// Package main. Dispatch is on argv[1] before any flag parsing, so a
// subcommand costs nothing in the flag set and works in a shell-less image
// where Docker's exec-form HEALTHCHECK runs an argv directly.
//
//   stowcloud serve            the server
//   stowcloud healthcheck      loopback TLS probe, exit 0 on ok or degraded
//   stowcloud preview-worker   the jailed decoder; never run by hand
//   stowcloud migrate          one-shot state migration from the Rust tree
//   stowcloud smb-render       write smb.conf and exit
func main()
```

The `healthcheck` semantics are carried over unchanged, including the part that
is easy to get wrong: exit 0 for both `ok` and `degraded`, and 1 only when the
server does not answer at all. A degraded server is a configuration state, and
mapping it to unhealthy makes Docker restart-loop a problem forever.

### 5-2. Error Handling

| Exit code | Meaning |
|---|---|
| 0 | success, or `healthcheck` reaching a server that answered |
| 1 | `healthcheck` could not get a well-formed answer |
| 64 | usage error, unknown subcommand |
| 78 | configuration refused, including D3's hardening refusal |

## 6. Implementation Plan

### 6-1. Milestones

| Phase | Task | Size | Depends on | Owner |
|---|---|---|---|---|
| Phase 0a | `go.mod`, `cmd/stowcloud` with dispatch and version, the six D-rule packages as empty-but-compiling | S | none | heavycaffeiner |
| Phase 0b | `golangci-lint` configuration, the two in-tree analysers, the gate's Go half, the CI job | S | 0a | heavycaffeiner |
| Phase 0c | A1 to A4 settled, answers written back into documents 3, 4, 5 and 12 | S | 0a | heavycaffeiner |

0b and 0c are independent of each other.

### 6-2. Dependencies

**Toolchain.** Go 1.24 or newer, which is the floor for the language features
used here rather than a preference; the current stable release is what CI pins.
`CGO_ENABLED=0` everywhere. No C compiler, no cross-linker, no `zig cc`, which
deletes `tools/zigcc-musl.ps1` and `scripts/musl-env.sh` at cutover.

**Tools**, installed by the gate: `golangci-lint`, `govulncheck`.

**Nothing else in this phase.** The module list in the parent proposal arrives
with the phases that need it, so `go.sum` at the end of Phase 0 has `x/sys` and
nothing more.

## 7. References

- `scripts/verify.sh`: the two rules the Go half keeps, and the four steps it
  deletes, with the comments explaining why each exists.
- `scripts/musl-env.sh`, `tools/zigcc-musl.ps1`: deleted at cutover.
- `Dockerfile`: the stage structure kept, the builder stage replaced, and the
  comment about a second binary that C2 makes obsolete.
- `crates/sc-server/src/main.rs`: the `healthcheck` argv interception this
  generalises to four subcommands.
- `crates/sc-vfs/src/backend/linux.rs:548` and `:304`: the two comments quoted
  in §4.3.3, both recording a bug the portable backend hid.
- Go: `go help build`, `//go:embed`, `go vet` analyser API, `go version -m`.

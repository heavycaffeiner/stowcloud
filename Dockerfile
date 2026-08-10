# syntax=docker/dockerfile:1.7
#
# Builds `sc:core` per `docs/DEPLOYMENT.md` §1:
#   - one statically linked binary (sc-server) on
#     x86_64-unknown-linux-musl / aarch64-unknown-linux-musl
#   - frontend embedded at compile time (`--features embed-ui`)
#   - gcr.io/distroless/static base, < 30 MB target
#
# ============================================================================
# One binary, not the two DEPLOYMENT.md §1 originally described
# ============================================================================
# `crates/sc-preview/src/worker/jailed/mod.rs` forks preview workers from the
# running `sc-server` process (plain `fork(2)`, no `execve`); there is no
# `sc-preview-worker` source, binary, or Cargo target anywhere in this
# workspace. Its own module doc says a re-`exec`'d dedicated binary is "a
# strictly better shape for production" and "deliberately left as follow-up".
# This Dockerfile builds the one real binary (`sc-server`) and does not
# invent the second. When `sc-preview-worker` exists, add a second `cargo
# auditable build ... --bin sc-preview-worker` line to the builder stage and
# a second `COPY --from=builder` line to the runtime stage; nothing else
# about this file's structure should need to change.
#
# ============================================================================
# The global allocator
# ============================================================================
# DEPLOYMENT.md §1 mandates mimalloc as the global allocator for the musl
# target. `crates/sc-server/src/main.rs` declares
# `#[cfg(target_env = "musl")] #[global_allocator] static GLOBAL_ALLOCATOR:
# mimalloc::MiMalloc = ...`, and `crates/sc-server/Cargo.toml` pulls the
# `mimalloc` crate in only for that target (`[target.'cfg(target_env =
# "musl")'.dependencies]`) — this turned out not to require touching
# `lib.rs`/`routes.rs`/`app.rs`/`bridge.rs` at all, since `main.rs` is the
# binary crate's own root and a `#[global_allocator]` is a whole-program,
# link-time setting. See `main.rs` for the full reasoning, including a
# measured (not asserted) before/after: a standalone small-allocation,
# multi-threaded microbenchmark run on this project's Linux test VM (glibc,
# virtualized) showed mimalloc ~3-4x *slower* than the default allocator —
# a real result, but for glibc, not musl, which is the comparison
# DEPLOYMENT.md §1 actually makes. No development environment here can link
# and execute a real musl binary to test that comparison directly
# (see `main.rs` for exactly why) — re-measure inside
# this image, once Docker is available, before trusting the doc's
# throughput claim at face value.
#
# ============================================================================
# Multi-arch strategy
# ============================================================================
# Built via `docker buildx build --platform linux/amd64,linux/arm64`. Under
# buildx, each platform's stages run **natively** (QEMU-emulated when the
# host arch differs), so `x86_64-unknown-linux-musl` is built on an amd64
# container and `aarch64-unknown-linux-musl` on an arm64 one — same
# architecture as the build host in every case, only the libc differs
# (glibc host toolchain -> musl target). That's a much smaller gap than the
# Windows dev box's problem (`tools/zigcc-musl.ps1`, cross-*architecture*
# via `zig cc`), so plain `musl-tools` (native `musl-gcc`) suffices here; zig
# is not needed in this image.

# node 24, not 22, for the npm major it bundles. `web/package-lock.json` was
# written by npm 11; node:22-alpine ships npm 10.9.8, which reads that same
# lockfile as out of sync and fails `npm ci` with `Missing: picomatch@4.0.5
# from lock file` — a nested, transitive entry (tinyglobby's and vite's own
# picomatch) that npm 10 does not resolve the way npm 11 recorded it. The
# lockfile is not corrupt: `npm ci` on this exact pair succeeds under npm
# 11.16 (node:24-alpine) and under npm 11.19 installed into node:22-alpine,
# and fails only under 10.9.8. Verified 2026-07-31 by running all three in
# the Rocky guest. Regenerating the lockfile with npm 10 would fix the build
# and silently downgrade what every developer here already has installed
# (npm 11), so the image moves instead.
#
# Digest moved 2026-07-31: the previous index (a0b9bf06..., built 2026-06-24)
# carried Node 24.18.0, and 24.18.1 — released 2026-07-28 — is a *security*
# release. Read out of the registry rather than assumed: the config blob
# behind each index reports NODE_VERSION=24.18.0 and 24.18.1 respectively.
# Staying on the 24 line on purpose. 26.5.1 is newer but v26 is still a
# `Current` line; 24.18.1 is Active LTS ("Krypton", maintained to 2028-04)
# and carries the same 2026-07-28 security fixes, which is the trade a
# production image should take. This is an index digest, so buildx resolves
# the platform itself (unlike DISTROLESS_DIGEST below, which is per-arch):
#   amd64: sha256:9b6d6e32fdbed527c0492b8e2d9d4c9081644a080b772670816bec13ba50b683
#   arm64: sha256:9f28ad37052908fb889f5c2494ae572eeeba9a94964ef9f8c735ef088c4ec3e9
ARG NODE_IMAGE=node:24-alpine@sha256:f70403e87646dc51b45295f4b8b70cdad0b63d2297c4c9899119b03f7af7a6b3
# 1.88, not 1.85. `Cargo.lock` resolves `image@0.25.10` and `time@0.3.54`,
# both of which declare `rust-version = 1.88.0` (plus the icu_* family at
# 1.86), so `cargo build --locked` under 1.85 fails outright with "rustc
# 1.85.1 is not supported by the following packages" before compiling
# anything. The workspace's own `rust-version` was 1.85 and simply no longer
# matched what its lockfile needs — see Cargo.toml, raised to 1.88 in the
# same commit. Digest verified 2026-07-31 by `docker pull` + `docker inspect`
# in the Rocky guest, and `rustc --version` inside it reports 1.88.0.
ARG RUST_IMAGE=rust:1.88-slim-bookworm@sha256:38bc5a86d998772d4aec2348656ed21438d20fcdce2795b56ca434cf21430d89
# gcr.io/distroless/static-debian12:nonroot, amd64 manifest digest, verified
# 2026-07-30 by an actual anonymous-token registry pull against gcr.io (GET
# /v2/distroless/static-debian12/manifests/nonroot with an OCI-index Accept
# header to get the index, then GET .../manifests/<this digest> to confirm
# it 200s) — not a registry-API-text guess. That distinction matters: an
# *earlier* digest recorded in this file the same way
# (sha256:23795be0fe67b7d47d1ee62b19c7db750152db627d5bbfa31307e892a7575bec)
# turned out to 404 against the real registry when checked — it was simply
# wrong, and would have failed `docker build` the first time anyone ran it,
# not merely "unconfirmed". This box still has no Docker (`docker version`
# -> command not found), so "verified" here means "confirmed the manifest
# exists and resolves via the registry's own HTTP API", not "confirmed by
# `docker pull` and `docker inspect`" — re-run that once Docker is available
# before trusting this for production, but it is at least known to exist,
# which the previous value was not.
#
# pass --build-arg DISTROLESS_DIGEST=sha256:37d3baf4e657ce79c144b69b69f60b8542f366a64fb7a49bd99f5444ef008bb0
# for the arm64 manifest when building that platform explicitly (see the CI
# workflow, which does this per-arch rather than trying to make one digest
# serve both — a manifest digest is platform-specific, unlike a tag).
# The multi-arch index itself (the `nonroot` tag) is
# sha256:f5b485ea962d9bd1186b2f6b3a061191539b905b82ec395de78cbfae51f20e35 as
# of the same verification, if you'd rather pin the index and let buildx
# resolve the platform automatically instead of a build-arg per arch.
ARG DISTROLESS_DIGEST=sha256:7a2bd171a18bdd39a4729600d0dca5f16e779d41156a6908b4f8a9a289e76d92

# ----------------------------------------------------------------------------
# Stage: frontend — `npm run build` produces web/build, which sc-http's
# `embed-ui` feature reads at *compile* time via `#[derive(RustEmbed)]`
# (`#[folder = "../../web/build/"]`, relative to crates/sc-http). This stage
# never sees web/.env.development (it isn't COPYed in, and it's build-context
# excluded by .dockerignore isn't needed for that reason alone — see below);
# `npm run build` runs Vite's default `production` mode regardless, which
# does not load `.env.development` even if it were present (Vite convention:
# `.env.<mode>` files load only in that mode). Do not add `--mode
# development` or `-- --mode development` to this RUN line, ever.
# web/src/lib/api/client.ts throws at module load as a second, independent
# check if VITE_API_MOCK=1 ever reaches a PROD build regardless.
# ----------------------------------------------------------------------------
FROM ${NODE_IMAGE} AS frontend
WORKDIR /src/web
COPY web/package.json web/package-lock.json ./
RUN npm ci
COPY web/ ./
RUN npm run build \
    && test -f build/index.html \
    && test -d build/app

# ----------------------------------------------------------------------------
# Stage: builder — static musl binary, release-dist profile
# (`Cargo.toml`: lto=fat, codegen-units=1, panic=abort, strip=true), SBOM
# embedded via `cargo auditable` (see docs/DEPLOYMENT.md addition for why
# that tool and not a bare cargo-sbom file).
# ----------------------------------------------------------------------------
FROM ${RUST_IMAGE} AS builder
ARG TARGETARCH
RUN case "${TARGETARCH}" in \
      amd64) echo x86_64-unknown-linux-musl > /rust_target.txt ;; \
      arm64) echo aarch64-unknown-linux-musl > /rust_target.txt ;; \
      *) echo "unsupported TARGETARCH=${TARGETARCH}" >&2; exit 1 ;; \
    esac

RUN apt-get update \
    && apt-get install -y --no-install-recommends musl-tools pkg-config ca-certificates \
    && rm -rf /var/lib/apt/lists/*

RUN rustup target add "$(cat /rust_target.txt)"
# rusqlite's "bundled" feature (every crate in this workspace that touches
# SQLite enables it — sc-server links it regardless) needs a C compiler for
# the musl target. `musl-tools` installs it under the literal name
# `musl-gcc` for whichever architecture this stage is *natively* running as
# (see the multi-arch note above), so the same two lines are correct on both
# platforms without an if/else.
ENV CC_x86_64_unknown_linux_musl=musl-gcc \
    CC_aarch64_unknown_linux_musl=musl-gcc \
    AR_x86_64_unknown_linux_musl=ar \
    AR_aarch64_unknown_linux_musl=ar

# cargo-auditable wraps `cargo build` byte-for-byte (same flags, same
# target dir) and additionally embeds a compressed JSON dependency manifest
# (crate name, version, source, `dep:`/`build-dep:` kind) as a
# `.rust-audit-info` ELF section in the output binary — see
# docs/DEPLOYMENT.md addition for the justification over a sidecar SBOM
# file. Pinned by tag, not floating: this line is the one place in this
# image that resolves a crates.io version at build time rather than reading
# a lockfile, so it's pinned explicitly.
# 0.7.5, matching supply-chain.yml's SBOM job: the workflow audits the
# dependency set this stage embeds, so a version skew between them would mean
# auditing one `.rust-audit-info` format and shipping another. Its locked
# dependencies top out at `cargo_metadata`'s 1.86, so it installs under this
# stage's 1.88 toolchain.
RUN cargo install cargo-auditable --version 0.7.5 --locked

WORKDIR /src
COPY Cargo.toml Cargo.lock ./
COPY crates/ ./crates/
COPY --from=frontend /src/web/build ./web/build

RUN mkdir -p /out && \
    RUST_TARGET="$(cat /rust_target.txt)" && \
    cargo auditable build --profile release-dist --locked \
      --target "${RUST_TARGET}" \
      -p sc-server --features embed-ui && \
    cp "target/${RUST_TARGET}/release-dist/sc-server" /out/sc-server

# ----------------------------------------------------------------------------
# Stage: runtime — distroless/static, nonroot variant (uid/gid 65532 by
# default; DEPLOYMENT.md §9/§10's `user: "1000:1000"` overrides this at
# `docker run`/compose time for share-mount ownership alignment — either
# way, this image is never root). No shell, no package manager, no
# `chown`/`curl`/`wget` — that is the point of distroless (§10 checklist).
#
# That used to also mean no `HEALTHCHECK CMD` — nothing in the image could
# execute an exec-form probe. It doesn't anymore: `/sc-server` itself now
# answers to a `healthcheck` argv[1] (`crates/sc-server/src/main.rs`,
# intercepted before clap ever runs, so it costs no new subcommand in
# `sc_server::Cli`) with a std-only loopback `GET /api/health`. Docker's
# exec-form `HEALTHCHECK CMD [...]` runs a listed argv directly — it does
# not go through `ENTRYPOINT`/`CMD` or a shell — so pointing it at the same
# binary with a different argument works in a shell-less image exactly as
# well as a dedicated healthcheck binary would.
# ----------------------------------------------------------------------------
FROM gcr.io/distroless/static-debian12@${DISTROLESS_DIGEST} AS runtime

COPY --from=builder --chown=nonroot:nonroot /out/sc-server /sc-server

# MIT, BSD, ISC and Apache-2.0 all require their notice to reach whoever
# receives a copy of the work, and for most people the copy they receive is
# this image, not the repo. Straight from the build context: the builder stage
# only ever COPYs Cargo manifests and crates/, so it does not have these.
COPY LICENSE /LICENSE
COPY THIRD-PARTY-NOTICES.md /THIRD-PARTY-NOTICES.md

# SC_BIND defaults to 127.0.0.1:8443 in sc-server's own Config::default()
# (crates/sc-server/src/config.rs) — correct for a bare-metal install, wrong
# in a container: Docker's published-port DNAT targets the container's
# bridge-network interface, not its loopback, so a server bound to
# 127.0.0.1 is unreachable from outside the container network namespace no
# matter what `ports:` says. This ENV is the fix; SC_BIND is one of the
# env vars `Config::apply_env` reads (`crates/sc-server/src/config.rs`),
# so compose/`docker run -e` can still override it, e.g. to bind a specific
# interface inside a `network_mode: host` deployment.
#
# Whatever address it names, that socket serves HTTPS and only HTTPS. There is
# no plaintext listener anywhere to publish by mistake (`Config::bind`), and
# the certificate is generated into /var/lib/sc/tls on first run. A plain HTTP
# request to the same port gets a 308 to the https URL, which is a redirect,
# not a second listener: no request is ever served in the clear.
ENV SC_BIND=0.0.0.0:8443 \
    SC_DATA_DIR=/var/lib/sc

# /var/lib/sc must exist and be writable by whatever uid the container
# actually runs as (see docker-compose.yml and the DEPLOYMENT.md addition —
# there is no entrypoint chown step, deliberately: distroless has no `chown`
# binary to run one with, and DEPLOYMENT.md §6.1 forbids `chown -R` on
# mounted volumes on principle anyway; the fix is pre-creating the host
# directory with the right owner, not doing it at container start).
VOLUME ["/var/lib/sc"]

EXPOSE 8443

# Exec form, no shell involved — reads SC_BIND itself to find the right
# port (see `run_healthcheck` in `crates/sc-server/src/main.rs`), so this
# instruction needs no edit if SC_BIND is overridden at run time.
# `--start-period` covers first-run master-key generation and SQLite
# migrations (`masterkey.rs`, `crates/sc-server/src/setup.rs`) before the
# first probe counts against `--retries`.
HEALTHCHECK --interval=30s --timeout=5s --start-period=10s --retries=3 \
  CMD ["/sc-server", "healthcheck"]

ENTRYPOINT ["/sc-server"]
CMD ["serve"]

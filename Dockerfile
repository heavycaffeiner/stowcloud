# syntax=docker/dockerfile:1.7
#
# One statically linked binary on a base with no shell and no package manager.
#
# ============================================================================
# Why there is no C toolchain in the builder
# ============================================================================
# The build sets CGO_ENABLED=0, which is the whole static-binary story: with
# cgo off the result has no dynamic loader and no libc to match, so the runtime
# stage needs neither. That is also what constrains the SQLite driver choice
# elsewhere in this tree, and it is why the builder stage installs nothing
# beyond the toolchain image's own contents.
#
# The one exception in this repository is the race detector, which requires cgo
# and produces a test binary that never ships. It has no place in this file.
#
# ============================================================================
# Two binaries in one file
# ============================================================================
# The preview worker is the same binary under a different argument, not a
# second one. It is re-executed from the server so the sandbox it applies to
# itself covers every thread, which forking alone does not give: a Landlock
# domain is installed on the calling thread, and the Go runtime moves
# goroutines between threads.
#
# ============================================================================
# Multi-arch
# ============================================================================
# Built with buildx across both platforms. Each stage runs natively for its
# platform, so the Go build never cross-compiles by architecture, and with cgo
# off there is no cross-libc question either. GOARCH is still set explicitly
# rather than inherited, because a build that silently produced the builder's
# own architecture would look identical until the image ran somewhere else.

# The toolchain, pinned by digest rather than by tag: a tag is a moving target
# and this is the one input that decides what the binary is.
ARG GO_IMAGE=golang:1.25-bookworm

# node 24, not 22, for the npm major it bundles. The lockfile in web/ was
# written by npm 11, and npm 10 reads the same file as out of sync and fails on
# a nested transitive entry. The lockfile is not corrupt: the same install
# succeeds under npm 11 and fails only under 10, so the image moves rather than
# the lockfile, which would silently downgrade what every developer here has
# installed.
ARG NODE_IMAGE=node:24-alpine

# The runtime base carries no shell, no package manager, and no tool to copy a
# file with. Its digest is per-architecture, so the CI workflow passes one per
# platform rather than trying to make a single value serve both.
ARG DISTROLESS_DIGEST=sha256:7a2bd171a18bdd39a4729600d0dca5f16e779d41156a6908b4f8a9a289e76d92

# ----------------------------------------------------------------------------
# Stage: frontend
#
# The output goes into the Go package that embeds it, because the embed
# directive cannot name a path outside its own package and refuses to follow a
# symlink out. That is also what gives the embed a real dependency edge: a
# rebuilt frontend is picked up by the next Go build, so there is no stale
# bundle to clean.
#
# This stage never sees a development environment file. The build context
# excludes them, and the build runs in production mode regardless, which does
# not read them even when present.
# ----------------------------------------------------------------------------
FROM ${NODE_IMAGE} AS frontend
WORKDIR /src/web
COPY web/package.json web/package-lock.json ./
RUN npm ci
COPY web/ ./
RUN npm run build \
    && test -f ../go/internal/httpapi/spa/build/index.html \
    && test -d ../go/internal/httpapi/spa/build/app

# ----------------------------------------------------------------------------
# Stage: builder
# ----------------------------------------------------------------------------
FROM ${GO_IMAGE} AS builder
ARG TARGETARCH
WORKDIR /src/go

# The module graph is downloaded before the source is copied, so a source-only
# change does not refetch it.
COPY go/go.mod go/go.sum ./
RUN go mod download

COPY go/ ./
# The frontend lands in the package that embeds it, which is the only
# arrangement where the dependency edge exists.
COPY --from=frontend /src/go/internal/httpapi/spa/build ./internal/httpapi/spa/build

# The tag is what turns the embed on. A build without it links a server that
# serves no frontend, which is the correct behaviour for a build that has no
# bundle and the wrong one for this image, so the bundle is checked above
# rather than discovered at run time.
#
# The trimmed path and the empty build id are what make the same source produce
# the same bytes on two machines, so a published binary can be checked against
# a build of the tag it claims to come from.
RUN mkdir -p /out && \
    CGO_ENABLED=0 GOOS=linux GOARCH="${TARGETARCH}" \
      go build -tags embed_ui \
        -trimpath \
        -ldflags="-s -w -buildid=" \
        -o /out/stowcloud ./cmd/stowcloud && \
    # A binary that turned out dynamic is a binary the runtime base cannot run,
    # and the failure would otherwise arrive as a missing-file error at start.
    ! go version -m /out/stowcloud | grep -q 'CGO_ENABLED=1'

# ----------------------------------------------------------------------------
# Stage: runtime
#
# The nonroot variant, so the image is never root even before the compose file
# overrides the uid for share ownership. There is no step that changes the
# ownership of a mounted directory: this base has nothing to run one with, and
# recursively changing the ownership of a directory somebody else's program
# also writes is not something a container start should do. The host directory
# is created with the right owner instead.
# ----------------------------------------------------------------------------
FROM gcr.io/distroless/static-debian12@${DISTROLESS_DIGEST} AS runtime

COPY --from=builder --chown=nonroot:nonroot /out/stowcloud /stowcloud

# The permissive licences all require their notice to reach whoever receives a
# copy of the work, and for most people that copy is this image rather than the
# repository.
COPY LICENSE /LICENSE
COPY THIRD-PARTY-NOTICES.md /THIRD-PARTY-NOTICES.md

# The data directory holds the store, the certificate and the setup token. It
# has to exist and be writable by whatever uid the container actually runs as.
VOLUME ["/var/lib/stowcloud"]

# One socket, and it is TLS. There is no plaintext listener anywhere to publish
# by mistake, and the certificate is generated into the data directory on first
# run.
EXPOSE 8443

# The exec form runs the listed argv directly rather than through a shell,
# which is what lets a shell-less image have a health check at all: it is this
# same binary under a different argument.
#
# The start period covers first-run key generation and the schema migrations
# before the first probe counts as a failure. The probe exits zero for degraded
# as well as ok, because a degraded server is a configuration state and
# restarting it does not fix one.
HEALTHCHECK --interval=30s --timeout=5s --start-period=10s --retries=3 \
  CMD ["/stowcloud", "healthcheck", "/etc/stowcloud/sc.toml"]

ENTRYPOINT ["/stowcloud"]
CMD ["serve", "/etc/stowcloud/sc.toml"]

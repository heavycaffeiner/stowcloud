# syntax=docker/dockerfile:1.7
#
# One statically linked binary on an Alpine base, running as an unprivileged
# uid the deployment's own files already carry.
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
#
# The same release CI compiles with, not go/go.mod's directive. That directive
# is the dependency floor and the two move independently, but the image and CI
# building with different compilers means the binary an image ships is not the
# one the gate ran against.
ARG GO_IMAGE=golang:1.26-bookworm

# node 24, not 22, for the npm major it bundles. The lockfile in web/ was
# written by npm 11, and npm 10 reads the same file as out of sync and fails on
# a nested transitive entry. The lockfile is not corrupt: the same install
# succeeds under npm 11 and fails only under 10, so the image moves rather than
# the lockfile, which would silently downgrade what every developer here has
# installed.
ARG NODE_IMAGE=node:24-alpine

# The runtime base. Alpine rather than distroless, and the same 3.24 the SMB
# sidecar pins, so both images track one base and one support window.
#
# The digest is per-architecture, so the CI workflow passes one per platform
# rather than trying to make a single value serve both. This default is the
# amd64 manifest, which is what a bare `docker build` on the usual laptop
# wants; passing the index digest instead yields an image of whichever
# architecture buildx resolved.
#   index: sha256:28bd5fe8b56d1bd048e5babf5b10710ebe0bae67db86916198a6eec434943f8b
#   amd64: sha256:79ff19e9084a00eece421b2523fb93e22d730e2c0e525905de047e848e56d95f
#   arm64: sha256:e7a1a92a5bfeee40966aea60f0796b0e7917cc35591542701834f03a68fa3d18
ARG ALPINE_DIGEST=sha256:79ff19e9084a00eece421b2523fb93e22d730e2c0e525905de047e848e56d95f

# The uid and gid the server runs as, and the ones the shipped directories are
# owned by. Named as the wider self-hosting ecosystem names them, because an
# operator who has set PUID and PGID on another image already knows what these
# do.
#
# 1000 rather than a service number in the 65000s: the folders a deployment
# mounts in are somebody's own files, and on a single-admin NAS those are
# almost always uid 1000. Matching it is what lets a bind mount work with no
# preparation at all.
#
# These are the build-time default. The entrypoint reads PUID and PGID from the
# environment at start and moves the account to match, so an operator changes
# the uid without rebuilding.
ARG PUID=1000
ARG PGID=1000

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
# The directories a fresh deployment mounts volumes over, staged here because
# they are owned by the build's uid rather than root. Copied in owned by the
# runtime uid: a named volume mounted over an existing directory inherits that
# directory's ownership, so the server can write to it. An empty volume with
# nothing underneath is created root-owned instead, and the first write fails.
#
# /shares/files is the quickstart's one share. A deployment serving its own
# directories bind-mounts them and this goes unused.
ARG PUID
ARG PGID
RUN mkdir -p /staged/var/lib/stowcloud /staged/shares/files /staged/config/smb && \
    chown -R "${PUID}:${PGID}" /staged

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
# The entrypoint starts as root, reconciles the service account with PUID and
# PGID, and drops to it before exec'ing the server. Nothing after that runs
# privileged.
#
# The chown it does is bounded to the directories this server owns. The shares
# are never touched: they are the operator's files, usually shared with other
# services, and a recursive chown on them is not reversible from inside a
# container.
# ----------------------------------------------------------------------------
FROM alpine:3.24@${ALPINE_DIGEST} AS runtime
ARG PUID
ARG PGID

# su-exec drops privileges without the process-supervision layer su and sudo
# bring: it execs, so the server is PID 1 and gets the stop signal directly.
# 13 KiB, and the only package this image adds; the entrypoint rewrites the two
# account files itself rather than pulling in shadow for usermod.
RUN apk add --no-cache su-exec \
    && rm -rf /var/cache/apk/*

# The account the server runs as. Alpine ships no uid 1000, so it is created
# here rather than named: a numeric USER with no passwd entry works, but leaves
# every `ls -l` inside the container printing a bare number and gives the
# process no home to resolve.
#
# -S makes it a system account with no password and no login shell. -h /
# because the working directory below is /, and a home this account cannot
# search is what made the previous base refuse to start under an overridden
# uid.
RUN addgroup -g "${PGID}" -S stowcloud \
    && adduser -u "${PUID}" -G stowcloud -S -H -h / -s /sbin/nologin stowcloud

COPY --from=builder --chown=${PUID}:${PGID} /out/stowcloud /stowcloud
COPY --chmod=755 deploy/entrypoint.sh /entrypoint.sh

# / rather than a home directory. Nothing here needs a writable working
# directory, and / is searchable by every uid, so a container started with an
# overridden `user:` fails on the directories it actually touches rather than
# on the first path the process resolves.
WORKDIR /

# The permissive licences all require their notice to reach whoever receives a
# copy of the work, and for most people that copy is this image rather than the
# repository.
COPY LICENSE /LICENSE
COPY THIRD-PARTY-NOTICES.md /THIRD-PARTY-NOTICES.md

# The data directory holds the store, the certificate and the setup token.
#
# It ships in the image, owned by the runtime uid. A named volume mounted over
# an existing directory inherits that directory's owner, which is what lets
# `docker compose up` work with no host-side preparation. A bind mount does not
# inherit anything, so a host directory still has to be created with the right
# owner.
COPY --from=builder --chown=${PUID}:${PGID} /staged/var/lib/stowcloud /var/lib/stowcloud
# The default share and the SMB render directory, for the same reason: a volume
# mounted over either inherits an owner the server can write as.
COPY --from=builder --chown=${PUID}:${PGID} /staged/shares/files /shares/files
COPY --from=builder --chown=${PUID}:${PGID} /staged/config/smb /config/smb
VOLUME ["/var/lib/stowcloud"]

# No USER: the entrypoint needs root to move the account and hand the data
# directory over, and drops to PUID:PGID with su-exec before the server starts.
# A `user:` in a compose file still works and skips the reconciliation, because
# the entrypoint refuses to chown what it does not own.
ENV PUID=${PUID} PGID=${PGID}

# One socket, and it is TLS. There is no plaintext listener anywhere to publish
# by mistake, and the certificate is generated into the data directory on first
# run.
EXPOSE 8443

# The exec form runs the listed argv directly rather than through a shell: the
# probe is this same binary under a different argument, so there is nothing to
# install and nothing that can disagree with the server about its own state.
#
# The start period covers first-run key generation and the schema migrations
# before the first probe counts as a failure. The probe exits zero for degraded
# as well as ok, because a degraded server is a configuration state and
# restarting it does not fix one.
HEALTHCHECK --interval=30s --timeout=5s --start-period=10s --retries=3 \
  CMD ["/entrypoint.sh", "healthcheck", "/etc/stowcloud/sc.toml"]

ENTRYPOINT ["/entrypoint.sh"]
CMD ["serve", "/etc/stowcloud/sc.toml"]

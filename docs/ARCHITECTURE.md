# Architecture

The Linux server starts at `backend/cmd/sc-engine/main.go`. Preflight loads the deployment settings and security policy, and the command applies that policy before calling `server.Run` in `backend/internal/server/server.go`. `server/open.go` constructs the services, `server/mount.go` attaches native API, WebDAV, public-link, compatibility, and SPA routes, and `server/listener.go` serves them on Hanami's managed listener generations. Native JSON operations use Gin or Huma; Huma exposes the typed operations through OpenAPI.

`backend/internal/db` owns SQLite state, cache, and journal files. `backend/internal/fs` owns filesystem and virtual filesystem operations. Each feature package, such as `backend/internal/files`, `uploads`, `shares`, and `admin`, holds its domain service and the handlers that serve it. `backend/internal/server/routes.go` registers every Gin route and Huma operation by access group. `backend/internal/config` holds the command-line arguments, the operator settings with their bounds and defaults, and the checks a settings change passes before it is saved.

Tests that boot a whole engine and drive it over HTTP live in `backend/test/e2e`, with the shared boot and sign-in helpers in `harness_test.go`. Tests beside the code in `backend/internal` are unit tests of that package.

The browser application lives in `frontend/src`. It currently uses React Router, React Query, Zustand, and mdui. The planned frontend restructure moves server data into React Query, URL state into the router, and mdui imports into one UI directory.

## Contracts retained during the restructure

- Keep `files` as one Go package. Its `Resolved` value has unexported fields and can only come from `Resolve`, which is part of the access-control boundary.
- Keep the share, device, inode, and birth-time identity tuple in one dependency-free package (`backend/internal/db/ident` today).
- Preserve durable formats: SQLite migration history, key-ring bytes and sealed-value bindings, instance and file ID derivation, change tokens, and upload resume records. New schema changes append migrations. The release v0.19.1 fixture in `backend/internal/db/state/testdata` checks an upgrade from schema 19.
- Keep native resource routes under `/api/v1` with one spelling per operation. Public links, WebDAV, and compatibility routes retain their own wire shapes.

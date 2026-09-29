# Architecture

The Linux server starts at `backend/cmd/sc-engine/main.go`. Preflight loads the deployment settings and security policy. Hanami owns process startup and listener generations. `backend/internal/app/open.go` constructs the services, and `app/mount.go` attaches native API, WebDAV, public-link, compatibility, and SPA routes. Native JSON operations use Gin or Huma; Huma exposes the typed operations through OpenAPI.

`backend/internal/store` owns SQLite state, cache, and journal files. `backend/internal/storage` owns filesystem and virtual filesystem operations. `backend/internal/feature` holds domain services, while `backend/internal/http` holds most transport handlers. The current route table, handler map, and Huma registration are separate. The planned restructure puts route registration in one server package.

The browser application lives in `frontend/src`. It currently uses React Router, React Query, Zustand, and mdui. The planned frontend restructure moves server data into React Query, URL state into the router, and mdui imports into one UI directory.

## Contracts retained during the restructure

- Keep `files` as one Go package. Its `Resolved` value has unexported fields and can only come from `Resolve`, which is part of the access-control boundary.
- Keep the share, device, inode, and birth-time identity tuple in one dependency-free package (`backend/internal/store/ident` today).
- Preserve durable formats: SQLite migration history, key-ring bytes and sealed-value bindings, instance and file ID derivation, change tokens, and upload resume records. New schema changes append migrations. The release v0.19.1 fixture in `backend/internal/store/state/testdata` checks an upgrade from schema 19.
- Keep native resource routes under `/api/v1` with one spelling per operation. Public links, WebDAV, and compatibility routes retain their own wire shapes.

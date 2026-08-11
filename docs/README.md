# Proposals

Every subsystem is specified as a proposal written from what is built, not
from what was planned. `Status: Implemented` means the code exists and the
proposal describes it; where the two disagreed while these were written, the
code won.

## Reading order

| Proposal | Content | When to read it |
|---|---|---|
| [12 — Architecture](proposals/stowcloud-12-architecture.md) | the five principles, crate layout, technology choices and what was rejected, product boundary | **First** |
| [2 — Core](proposals/stowcloud-2-core-vfs.md) | `SafePath`, the syscall contract, ACL evaluation, the virtual root, directory ETags, trash | before any core work |
| [11 — Footprint](proposals/stowcloud-11-footprint.md) | the 32 GB / 12 TB floor and the two findings that changed the schema | **before assuming a resource budget** |
| [10 — Auth](proposals/stowcloud-10-auth.md) | Argon2 parameters, the three-tier verification path, sessions, app passwords, TOTP, enumeration defence | before any auth work |
| [0 — OIDC login](proposals/stowcloud-0-oidc-login.md) | link-only single sign-on | with 10 |
| [9 — API](proposals/stowcloud-9-api.md) | the error envelope, listing sessions, the middleware order | API and frontend contract |
| [7 — Upload](proposals/stowcloud-7-upload.md) | TUS, the interval set, the ordering rule, compat chunking | before upload work |
| [4 — WebDAV](proposals/stowcloud-4-webdav.md) | RFC 4918 Class 2, XML hardening, streamed PROPFIND, locking, `/dav-uploads` | before WebDAV work |
| [8 — Compat](proposals/stowcloud-8-compat.md) | the isolation contract and the two gates that enforce it | before compat work |
| [14 - Mobile compat](proposals/stowcloud-14-compat-mobile.md) | search, favourites, trashbin and the account lifecycle the phone apps need | with 8, before mobile work |
| [15 - Sharing](proposals/stowcloud-15-sharing.md) | the two path vocabularies the share API mixed up, and what it advertises | before share work |
| [16 - Correctness sweep](proposals/stowcloud-16-correctness-sweep.md) | three path vocabularies with one name, three metadata writes that fail in silence, and folder sizes no client sees correctly | before 15, and before any path-handling work |
| [6 — Preview and sharing](proposals/stowcloud-6-preview-sharing.md) | the content origin, signed URLs, the worker jail, archives, share links | before preview or sharing work |
| [5 — Search](proposals/stowcloud-5-search.md) | the parallel walk, the optional trigram index, the estimator | before search work |
| [3 — Frontend](proposals/stowcloud-3-frontend.md) | virtual scroll, the upload worker, i18n, the byte budgets | before frontend work |
| [1 — SMB](proposals/stowcloud-1-smb.md) | the Samba sidecar, the uid contract, propagation, what SMB cannot express | before SMB work |
| [13 — Deployment](proposals/stowcloud-13-deployment.md) | seccomp reality, the filesystem gate, uid/gid, production topology | deployment and operations |
| [17 - Audit gaps](proposals/stowcloud-17-audit-gaps.md) | seven promises 0-13 make that the code does not keep, and what closes each | with 1, before any SMB credential work |
| [18 - Recent files](proposals/stowcloud-18-recent-files.md) | the recency query, why an ordered walk truncated at N is not the newest N, and the Recent Files destination | with 5 and 14, before any recency work |
| [19 - Share browsing](proposals/stowcloud-19-share-browsing.md) | the subpath a share link had no way to name, the folder download that never worked, and the viewer arrows that move the picture | with 6, before any public share-page work |

## Five principles, everything else follows from these

1. **The filesystem is the only source of truth.** The database is a cache you
   can delete and rebuild.
2. **A path is a kernel handle, not a string.** `openat2(RESOLVE_BENEATH)`
   removes TOCTOU by construction, not by convention.
3. **A shared folder is not ours.** Other services are assumed to be writing
   the same directory at the same time.
4. **The compat layer does not invade the core.** Enforced by a CI grep and a
   feature-stripped build, not by discipline.
5. **The default is least privilege.** No user homes, no symlinks, SMB off, no
   inline content rendering.

## Conventions

- Code comments do not cite these documents. A comment states its own reason,
  so the code stands alone; the proposal carries the long-form argument.
- A non-goal is recorded with the reasoning that made it one, in the proposal
  for the subsystem it belongs to.
- `scripts/verify.sh` is what decides whether a change is releasable.

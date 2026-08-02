# Design doc index

A lightweight file-management service on the native filesystem. Rust backend,
Svelte frontend. Linux / Docker only.

## Reading order

| Doc | Content | When to read it |
|---|---|---|
| [ARCHITECTURE.md](ARCHITECTURE.md) | Design principles, crate structure, full overview | **First** |
| [TECH-STACK.md](TECH-STACK.md) | Single reference table: technology, algorithms, crypto primitives, rejected alternatives | Quick lookup |
| [FEATURES.md](FEATURES.md) | Full feature inventory, each marked shipped / implemented-but-unreachable / non-goal | Scope check |
| [DESIGN-FOOTPRINT.md](DESIGN-FOOTPRINT.md) | 32 GB SSD / 12 TB RAID budget validation, DB size math, immediacy guarantees, HDD mitigations | **Before assuming a resource budget** |
| [DEPLOYMENT.md](DEPLOYMENT.md) | Docker seccomp reality, filesystem matrix, EXDEV, uid/gid, watch backends, Samba sidecar, Cloudflare | Deployment and operations |

## Five principles, everything else follows from these

1. **The filesystem is the only source of truth.** The DB is a cache you can
   delete and rebuild at any time.
2. **A path is a kernel handle, not a string.** `openat2(RESOLVE_BENEATH)`
   removes TOCTOU by construction, not by convention.
3. **A shared folder is not ours.** Jellyfin, `*arr`, rsync, and Samba are
   assumed to touch the same directory at the same time.
4. **The compat layer does not invade the core.** Test: *"would this feature
   need to exist without the compat layer?"* — enforced, not aspirational: see
   ARCHITECTURE.md §10.1 and the CI gate in `scripts/verify.sh` that greps
   core crates for `oc:`/`ocs`/`remote.php` and fails the build if it finds
   any.
5. **The default is least privilege.** No user homes, no symlinks, SMB off,
   no inline content rendering.

## Roadmap

See `ARCHITECTURE.md` §14. M1 (core) → M2 (web) → M3 (coexistence) → M4
(WebDAV) → M5 (compat layer) → M6 (SMB + hardening). All six are
reachable by a real client. What is still missing is listed there too:
Litmus conformance in CI, an automated sync-client regression suite,
and an external security review.

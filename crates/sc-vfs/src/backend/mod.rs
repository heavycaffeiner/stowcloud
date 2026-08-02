//! Two backends behind one interface.
//!
//! `linux`: the real, deployment-target implementation. `openat2` with
//! `RESOLVE_BENEATH`/`RESOLVE_NO_SYMLINKS`/`RESOLVE_IN_ROOT`, `statx`,
//! `getdents64`, `renameat2` — all via `rustix`.
//!
//! `portable`: a Windows/macOS/other-Unix dev-convenience fallback built on
//! `std::fs`, walking components explicitly and rejecting symlink escapes at
//! every step. It exists so the crate builds and its test suite runs on the
//! primary dev machine (Windows); it is not the hardened path.

#[cfg(target_os = "linux")]
pub(crate) mod linux;
#[cfg(target_os = "linux")]
pub(crate) use linux as imp;

#[cfg(not(target_os = "linux"))]
pub(crate) mod portable;
#[cfg(not(target_os = "linux"))]
pub(crate) use portable as imp;

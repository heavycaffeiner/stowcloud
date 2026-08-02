//! sc-vfs — kernel-handle based safe filesystem layer. This is the security
//! core of stowcloud: every filesystem access anywhere in the codebase goes
//! through a `ShareRoot` + `SafePath` (or a handle obtained from one).
//!
//! Authoritative specs:-2 (types, syscall mapping,
//! `SafePath` rejection table) and (why: path-as-kernel-
//! handle, not path-as-string).
//!
//! Two backends live behind `backend::imp`: `linux` (the real, hardened
//! `openat2(RESOLVE_BENEATH)` implementation — the deployment target) and
//! `portable` (a `std::fs` component-walk fallback so this crate builds and
//! its test suite runs on the primary dev machine, Windows). Nothing in this
//! module's public API differs between the two; only `cfg(target_os =
//! "linux")` selects which one compiles in.

mod backend;
mod caps;
mod copy;
mod error;
mod handle;
pub mod ids;
mod reserved;
mod safe_path;
mod share_root;
mod types;

pub use caps::{detect_kernel_caps, KernelCaps};
pub use error::VfsError;
pub use handle::{DirHandle, FileHandle};
pub use ids::*;
pub use reserved::{is_reserved_name, RESERVED_PREFIXES};
pub use safe_path::SafePath;
pub use share_root::ShareRoot;
pub use types::{DirEntry, FsType, IdStrategy, Kind, SharePolicy, Stat, SymlinkPolicy, TrashMode};

//! `sc-preview` -- content preview generation engine and archive-listing
//! logic.
//!
//! This crate is deliberately self-contained: decode/encode, caching,
//! worker-pool abstraction, and archive listing only. It knows nothing
//! about HTTP, signed URLs, or share links -- `sc-http` is expected to call
//! into [`PreviewService`] and [`archive::list_archive`] from its content
//! origin routes ( section 2.2 describes the token format that
//! gates access to those routes, but issuing/verifying that token is
//! `sc-http`'s job, not this crate's).
//!
//! OCR is out of scope. Video thumbnailing (ffmpeg subprocess,
//! section 4.4) is represented in the worker protocol
//! (`worker::JobKind::Video`) but not implemented. Running ffmpeg would need
//! either relaxing the worker's seccomp/Landlock jail (refused -- that jail
//! is proven on real hardware, see `worker::jailed`'s module docs and
//! `examples/jail_proof.rs`) or standing up a second, differently-shaped one
//! (`execve` + a per-file Landlock rule); both are future work. What the
//! crate *does* guarantee: a video
//! file is identified by magic bytes (`sniff::Sniffed::is_video`, never by
//! extension), routed to `worker::JobKind::Video`, and refused there with
//! `error::NegativeReason::Unimplemented` -- a correct, actionable answer
//! rather than a video file silently falling into the image decoder and
//! coming back looking like a corrupt image (`error::PreviewError::UnsupportedFormat`/
//! `DecodeError`). Both `InProcessWorkerPool` and `worker::jailed`'s real
//! worker report it identically.
//!
//! # Module map
//!
//! - [`sniff`] -- magic-byte MIME sniffing (section 3). Never trust an
//!   extension or a caller-supplied `Content-Type`.
//! - [`decode`] -- bomb-protected image decode (section 4.3): header-only
//!   dimension check before any pixel buffer is allocated.
//! - [`exif_strip`] -- EXIF orientation read + strip-everything-else
//!   (section 4.3).
//! - [`preset`] -- size-preset rounding (section 4.3).
//! - [`pipeline`] -- decode -> reorient -> resize -> encode, glued
//!   together.
//! - [`worker`] -- the `SCM_RIGHTS` job protocol types, the [`worker::WorkerPool`]
//!   trait boundary, `worker::jailed::JailedWorkerPool` (Linux: forked worker
//!   processes behind Landlock + rlimits + a seccomp allow-list, section 4.2 --
//!   proven by `examples/jail_proof.rs` on a real kernel), and
//!   [`worker::InProcessWorkerPool`] (the unsandboxed non-Linux fallback,
//!   also what this crate's Windows test suite exercises).
//! - [`cache`] -- LRU + negative cache + single-flight (section 6).
//! - [`service`] -- [`PreviewService`], the API `sc-http` is expected to
//!   call.
//! - [`archive`] -- ZIP listing with zip-slip/zip-bomb protection
//!   (section 5).

pub mod archive;
pub mod cache;
pub mod decode;
pub mod error;
pub mod exif_strip;
pub mod pipeline;
pub mod preset;
pub mod service;
pub mod sniff;
pub mod worker;

pub use archive::{list_archive, ArchiveEntry, ArchiveEntryKind, ArchiveLimits};
pub use cache::{CacheConfig, Key as CacheKey};
pub use decode::DecodeLimits;
pub use error::{NegativeReason, PreviewError};
pub use pipeline::generate_preview_bytes;
pub use preset::{round_to_preset, MAX_PRESET, PRESETS};
pub use service::{PreviewConfig, PreviewService};
pub use sniff::{sniff_head, sniff_reader, Sniffed};
pub use worker::{InProcessWorkerPool, JobKind, JobRequest, JobResponse, JobResult, WorkerPool};

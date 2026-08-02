//! Worker process protocol and pool trait (DESIGN-PREVIEW.md section 4).
//!
//! The real deployment target forks worker processes ahead of time and hands
//! them jobs over a Unix socket using `SCM_RIGHTS` fd passing: the parent
//! never sends a path, only already-open file descriptors, which makes path
//! traversal out of the worker's control structurally impossible.
//! [`JobRequest`]/[`JobResponse`] are the plain, serializable message shapes
//! for that protocol.
//!
//! [`WorkerPool`] is the trait boundary between "here is a job and two open
//! files" and however it actually gets executed. Two implementations ship:
//!
//! - [`jailed::JailedWorkerPool`] (Linux): the real thing. Worker children
//!   are forked at startup, sealed behind Landlock + rlimits + a seccomp-BPF
//!   allow-list, and fed jobs over `SOCK_SEQPACKET` with the input and output
//!   descriptors passed as `SCM_RIGHTS`. This is what `sc-server` uses on
//!   Linux, and what `DESIGN-PREVIEW.md` §4 is describing.
//! - [`InProcessWorkerPool`]: runs the decode/resize/encode pipeline
//!   in-process, no fork, no sandboxing. The non-Linux backend, and what
//!   this crate's Windows-hosted test suite exercises. It offers **no**
//!   containment: a memory-corruption bug in a decoder is a compromise of
//!   the whole server process. That is the trade `DESIGN-PREVIEW.md` §4.1
//!   exists to refuse, and it is accepted here only because Landlock and
//!   seccomp have no Windows equivalent.

use std::fs::File;
use std::io::{Read, Seek, SeekFrom, Write};

use serde::{Deserialize, Serialize};

use crate::decode::DecodeLimits;
use crate::error::{NegativeReason, PreviewError};
use crate::pipeline;

#[cfg(target_os = "linux")]
pub mod jailed;

/// Shared between [`InProcessWorkerPool::run_job`] and
/// `jailed::run_job_in_worker` so the two backends refuse a video job with
/// the exact same, honest message rather than two copies that could drift
/// apart (`DESIGN-PREVIEW.md` §4.4).
pub(crate) const VIDEO_UNIMPLEMENTED_REASON: &str = "video preview generation is not implemented in this build";

#[derive(Debug, Clone, Copy, PartialEq, Eq, Serialize, Deserialize)]
pub enum JobKind {
    Image,
    Video,
}

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct JobRequest {
    pub job_id: u64,
    pub kind: JobKind,
    pub target_w: u32,
    pub target_h: u32,
}

/// The worker's own verdict on the job. Distinct from the outer
/// `Result<JobResponse, PreviewError>` on [`WorkerPool::run_job`]: that
/// outer `Result` is for transport-level failures (the worker crashed, the
/// pipe broke); `JobResult` is the worker successfully running to
/// completion and reporting success or a decode/encode failure.
///
/// `Err` carries `kind: NegativeReason` alongside the message, not just a
/// bare string. The worker has the fully-typed `PreviewError` in hand right
/// up until the moment it crosses this wire — `NegativeReason` is what
/// survives the trip. Losing it here (i.e. going back to `Err(String)`) is
/// exactly the bug this fixes: every job-level failure, video's
/// "unimplemented" included, used to arrive at the caller indistinguishable
/// from a generic decode error, because a bare string was all a caller on
/// the other side of `SCM_RIGHTS` ever had to classify with.
#[derive(Debug, Clone, Serialize, Deserialize)]
pub enum JobResult {
    Ok { bytes_written: u64 },
    Err { kind: NegativeReason, reason: String },
}

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct JobResponse {
    pub job_id: u64,
    pub result: JobResult,
}

/// A pool capable of running preview-generation jobs. Implementations own
/// however jobs actually get executed (in-process, forked + jailed
/// subprocess, ...); callers only see "request in, response out" plus two
/// already-open files for input/output, matching the fd-passing protocol
/// this trait is modeling.
pub trait WorkerPool: Send + Sync {
    fn run_job(&self, req: JobRequest, input: File, output: File) -> Result<JobResponse, PreviewError>;
}

/// Non-Linux backend: calls the decode/resize/encode pipeline directly in
/// the calling process. No fork, no Landlock, no seccomp — and therefore no
/// containment, which is the whole reason [`jailed::JailedWorkerPool`]
/// exists. This is the pool this crate's test suite exercises and the one a
/// `sc-server` running on Windows uses.
pub struct InProcessWorkerPool {
    pub limits: DecodeLimits,
}

impl InProcessWorkerPool {
    pub fn new(limits: DecodeLimits) -> Self {
        Self { limits }
    }
}

impl Default for InProcessWorkerPool {
    fn default() -> Self {
        Self::new(DecodeLimits::default())
    }
}

impl WorkerPool for InProcessWorkerPool {
    fn run_job(&self, req: JobRequest, mut input: File, mut output: File) -> Result<JobResponse, PreviewError> {
        if req.kind == JobKind::Video {
            // No jail can run ffmpeg without either relaxing the allow-list
            // (not on the table) or standing up a second, differently-shaped
            // jail (`execve` + a per-file Landlock rule; real future work,
            // not attempted here — see `jailed::run_job_in_worker`'s doc
            // comment). `NegativeReason::Unimplemented` — not `DecodeError`
            // — is what makes this a correct, actionable answer rather than
            // a video file looking like a corrupt image.
            return Ok(JobResponse {
                job_id: req.job_id,
                result: JobResult::Err {
                    kind: NegativeReason::Unimplemented,
                    reason: VIDEO_UNIMPLEMENTED_REASON.into(),
                },
            });
        }

        let mut raw = Vec::new();
        input.seek(SeekFrom::Start(0)).map_err(PreviewError::Io)?;
        input.read_to_end(&mut raw).map_err(PreviewError::Io)?;

        match pipeline::generate_preview_bytes(&raw, req.target_w, req.target_h, &self.limits) {
            Ok(bytes) => {
                output.write_all(&bytes).map_err(PreviewError::Io)?;
                output.flush().map_err(PreviewError::Io)?;
                Ok(JobResponse {
                    job_id: req.job_id,
                    result: JobResult::Ok {
                        bytes_written: bytes.len() as u64,
                    },
                })
            }
            Err(e) => Ok(JobResponse {
                job_id: req.job_id,
                result: JobResult::Err { kind: e.classify(), reason: e.to_string() },
            }),
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use std::io::Seek;

    fn tempfile_with(bytes: &[u8]) -> File {
        let mut f = tempfile::tempfile().unwrap();
        f.write_all(bytes).unwrap();
        f.seek(SeekFrom::Start(0)).unwrap();
        f
    }

    #[test]
    fn runs_an_image_job_end_to_end_through_files() {
        let mut img = image::RgbImage::from_pixel(64, 64, image::Rgb([9, 9, 9]));
        img.put_pixel(0, 0, image::Rgb([200, 0, 0]));
        let mut raw = Vec::new();
        image::DynamicImage::ImageRgb8(img)
            .write_to(&mut std::io::Cursor::new(&mut raw), image::ImageFormat::Png)
            .unwrap();

        let input = tempfile_with(&raw);
        let output = tempfile::tempfile().unwrap();

        let pool = InProcessWorkerPool::default();
        let resp = pool
            .run_job(
                JobRequest {
                    job_id: 1,
                    kind: JobKind::Image,
                    target_w: 32,
                    target_h: 32,
                },
                input,
                output.try_clone().unwrap(),
            )
            .unwrap();

        match resp.result {
            JobResult::Ok { bytes_written } => assert!(bytes_written > 0),
            JobResult::Err { reason, .. } => panic!("expected success, got {reason}"),
        }

        let mut out = output;
        out.seek(SeekFrom::Start(0)).unwrap();
        let mut produced = Vec::new();
        out.read_to_end(&mut produced).unwrap();
        let decoded = image::load_from_memory_with_format(&produced, image::ImageFormat::WebP).unwrap();
        assert!(decoded.width() <= 32 && decoded.height() <= 32);
    }

    #[test]
    fn video_jobs_report_a_job_level_failure_not_a_pool_error() {
        let input = tempfile_with(b"not actually a video");
        let output = tempfile::tempfile().unwrap();
        let pool = InProcessWorkerPool::default();
        let resp = pool
            .run_job(
                JobRequest {
                    job_id: 7,
                    kind: JobKind::Video,
                    target_w: 128,
                    target_h: 128,
                },
                input,
                output,
            )
            .unwrap();
        assert!(matches!(resp.result, JobResult::Err { .. }));
    }

    /// Before this fix, a video job's refusal reached the caller as a bare
    /// `reason: String` with no classification, and `PreviewService` always
    /// wrapped *any* `JobResult::Err` as `PreviewError::Decode(..)` — so a
    /// video file and a corrupt image were indistinguishable by the time
    /// either reached the negative cache or the client. `kind` must be
    /// `Unimplemented`, not `DecodeError`, for the refusal to be an
    /// *honest* answer rather than a misleading one.
    #[test]
    fn a_video_jobs_failure_is_classified_as_unimplemented_not_a_decode_error() {
        let input = tempfile_with(b"not actually a video");
        let output = tempfile::tempfile().unwrap();
        let pool = InProcessWorkerPool::default();
        let resp = pool
            .run_job(
                JobRequest {
                    job_id: 8,
                    kind: JobKind::Video,
                    target_w: 128,
                    target_h: 128,
                },
                input,
                output,
            )
            .unwrap();
        match resp.result {
            JobResult::Err { kind, reason } => {
                assert_eq!(kind, NegativeReason::Unimplemented, "reason was: {reason}");
                assert_eq!(reason, VIDEO_UNIMPLEMENTED_REASON);
            }
            JobResult::Ok { .. } => panic!("a video job must not succeed in this build"),
        }
    }
}

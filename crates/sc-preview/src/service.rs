//! `PreviewService` -- the public entry point `sc-http` (or any other
//! caller) is meant to use: cache lookups, single-flight de-duplication,
//! and a global concurrency cap around calls into a [`WorkerPool`].

use std::io::Read;
use std::sync::Arc;

use bytes::Bytes;
use sc_vfs::ids::FileId;
use tokio::sync::Semaphore;

use crate::cache::{CacheConfig, Key, PreviewCache};
use crate::decode::DecodeLimits;
use crate::error::PreviewError;
use crate::preset::round_to_preset;
use crate::sniff::sniff_head;
use crate::worker::{JobKind, JobRequest, JobResult, WorkerPool};

pub struct PreviewConfig {
    pub cache: CacheConfig,
    pub decode_limits: DecodeLimits,
    /// Global cap on concurrently *running* generations (
    /// section 6: "generation concurrency limit: a global semaphore, default =
    /// core count / 2"). Does not limit how many callers can be waiting --
    /// only how many are actually inside the worker pool at once.
    pub max_concurrent_generations: usize,
}

impl Default for PreviewConfig {
    fn default() -> Self {
        Self {
            cache: CacheConfig::default(),
            decode_limits: DecodeLimits::default(),
            max_concurrent_generations: (std::thread::available_parallelism()
                .map(|n| n.get())
                .unwrap_or(4)
                / 2)
            .max(1),
        }
    }
}

pub struct PreviewService {
    cache: PreviewCache,
    pool: Arc<dyn WorkerPool>,
    semaphore: Semaphore,
    next_job_id: std::sync::atomic::AtomicU64,
}

impl PreviewService {
    pub fn new(cfg: PreviewConfig, pool: Arc<dyn WorkerPool>) -> Self {
        Self {
            cache: PreviewCache::new(cfg.cache),
            pool,
            semaphore: Semaphore::new(cfg.max_concurrent_generations.max(1)),
            next_job_id: std::sync::atomic::AtomicU64::new(1),
        }
    }

    /// Returns cached or freshly-generated WebP preview bytes for
    /// `(fileid, w, h, etag8)`. `(w, h)` are rounded up to the nearest
    /// preset (`crate::preset::round_to_preset`) before anything else
    /// happens, so requests for e.g. 100x100 and 120x120 hit the same
    /// cache entry.
    ///
    /// `input` is read to completion eagerly (every caller pays this cost,
    /// not just whichever one ends up generating -- callers are expected to
    /// hand in a cheap-to-read source, e.g. a `File` they already have
    /// open, or a `Cursor` over bytes already in memory).
    pub async fn get_or_generate(
        &self,
        fileid: FileId,
        w: u32,
        h: u32,
        etag8: [u8; 8],
        mut input: impl Read,
    ) -> Result<Bytes, PreviewError> {
        let (target_w, target_h) = round_to_preset(w, h);
        let key = Key {
            fileid,
            w: target_w,
            h: target_h,
            etag8,
        };

        let mut raw = Vec::new();
        input.read_to_end(&mut raw).map_err(PreviewError::Io)?;

        // decide by magic bytes, never by extension
        // or a caller-supplied type — the same rule `sniff` already enforces
        // for the XSS-relevant MIME decision applies to picking the job kind.
        // This is also what makes a video file actually reach
        // `JobKind::Video` (and so the worker's honest "not implemented"
        // answer) instead of silently going through the image pipeline and
        // coming back as an opaque `UnsupportedFormat`.
        let kind = if sniff_head(&raw).is_video() { JobKind::Video } else { JobKind::Image };

        let pool = self.pool.clone();
        let semaphore = &self.semaphore;
        let job_id = self.next_job_id.fetch_add(1, std::sync::atomic::Ordering::SeqCst);

        self.cache
            .get_or_generate(key, || async move {
                let _permit = semaphore
                    .acquire()
                    .await
                    .map_err(|e| PreviewError::Worker(e.to_string()))?;

                run_worker_job(pool, job_id, kind, target_w, target_h, raw).await
            })
            .await
    }

    /// Test/observability hook: total number of times the underlying
    /// generator has actually run (across all keys), used to assert
    /// single-flight behavior end to end through the service.
    pub fn generation_call_count(&self) -> u64 {
        self.cache.generation_call_count()
    }
}

/// Generate a fresh, unique scratch-file path under the system temp
/// directory. Deliberately hand-rolled instead of depending on the
/// `tempfile` crate: that crate is a dev-only dependency here (see
/// `Cargo.toml`), reserved for test code, so production code paths use this
/// small helper plus best-effort cleanup (`ScratchCleanup`) instead.
fn scratch_path() -> std::path::PathBuf {
    std::env::temp_dir().join(format!("sc-preview-{}.tmp", uuid::Uuid::new_v4()))
}

fn open_scratch_file(path: &std::path::Path) -> std::io::Result<std::fs::File> {
    std::fs::OpenOptions::new()
        .read(true)
        .write(true)
        .create_new(true)
        .open(path)
}

async fn run_worker_job(
    pool: Arc<dyn WorkerPool>,
    job_id: u64,
    kind: JobKind,
    target_w: u32,
    target_h: u32,
    raw: Vec<u8>,
) -> Result<Bytes, PreviewError> {
    tokio::task::spawn_blocking(move || {
        let input_path = scratch_path();
        let output_path = scratch_path();
        // Declared before `input`/`output` below so it drops *after* them
        // (Rust drops locals in reverse declaration order) -- the scratch
        // files must be closed before we try to delete them, or the delete
        // is liable to fail on Windows (no FILE_SHARE_DELETE by default).
        let _cleanup = ScratchCleanup(vec![input_path.clone(), output_path.clone()]);

        let mut input = open_scratch_file(&input_path).map_err(PreviewError::Io)?;
        let output = open_scratch_file(&output_path).map_err(PreviewError::Io)?;

        std::io::Write::write_all(&mut input, &raw).map_err(PreviewError::Io)?;
        std::io::Seek::seek(&mut input, std::io::SeekFrom::Start(0)).map_err(PreviewError::Io)?;

        let resp = pool.run_job(
            JobRequest {
                job_id,
                kind,
                target_w,
                target_h,
            },
            input,
            output.try_clone().map_err(PreviewError::Io)?,
        )?;

        match resp.result {
            JobResult::Ok { .. } => {
                let mut out = output;
                std::io::Seek::seek(&mut out, std::io::SeekFrom::Start(0)).map_err(PreviewError::Io)?;
                let mut bytes = Vec::new();
                out.read_to_end(&mut bytes).map_err(PreviewError::Io)?;
                Ok(Bytes::from(bytes))
            }
            // `kind` is what the worker classified the failure as via its own
            // `PreviewError::classify()`, *before* the flattening down to a
            // wire-safe `reason: String` — see `PreviewError::FromWorker`'s
            // doc comment for why reconstructing e.g. always `Decode(reason)`
            // here would silently throw that classification away again.
            JobResult::Err { kind, reason } => Err(PreviewError::FromWorker { kind, reason }),
        }
    })
    .await
    .map_err(|e| PreviewError::Worker(format!("worker task panicked: {e}")))?
}

/// Best-effort scratch file cleanup on drop.
struct ScratchCleanup(Vec<std::path::PathBuf>);

impl Drop for ScratchCleanup {
    fn drop(&mut self) {
        for path in &self.0 {
            let _ = std::fs::remove_file(path);
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::error::NegativeReason;
    use crate::worker::{InProcessWorkerPool, JobResponse};
    use std::sync::atomic::{AtomicUsize, Ordering};
    use std::time::Duration;

    fn png_bytes(w: u32, h: u32) -> Vec<u8> {
        let img = image::RgbImage::from_pixel(w, h, image::Rgb([5, 5, 5]));
        let mut out = Vec::new();
        image::DynamicImage::ImageRgb8(img)
            .write_to(&mut std::io::Cursor::new(&mut out), image::ImageFormat::Png)
            .unwrap();
        out
    }

    /// End-to-end proof for the video-preview fix: a real caller
    /// (`PreviewService::get_or_generate`, the same entry point
    /// `sc-server`'s `ContentBridge::thumbnail` uses) handed WebM magic
    /// bytes must come back with an honest, correctly classified error —
    /// not the generic `UnsupportedFormat`/`DecodeError` a video file used
    /// to get by falling through to the image pipeline (`JobKind` was
    /// hardcoded to `Image` before this fix, so nothing ever exercised the
    /// worker's video branch from here).
    #[tokio::test]
    async fn a_video_file_is_refused_honestly_end_to_end() {
        let service = PreviewService::new(PreviewConfig::default(), Arc::new(InProcessWorkerPool::default()));
        // `infer`'s WebM matcher only inspects these four bytes; see
        // `sniff::tests::sniffs_webm_by_magic_bytes_as_video_not_image`.
        let webm_head = vec![0x1A, 0x45, 0xDF, 0xA3, 0, 0, 0, 0];

        let err = service
            .get_or_generate(FileId::new(42), 100, 100, [0; 8], std::io::Cursor::new(webm_head))
            .await
            .expect_err("video preview generation must fail, not silently succeed");

        assert_eq!(
            err.classify(),
            NegativeReason::Unimplemented,
            "a video file must classify as Unimplemented, not DecodeError -- got {err:?}"
        );
    }

    #[tokio::test]
    async fn generates_and_then_serves_from_cache() {
        let service = PreviewService::new(PreviewConfig::default(), Arc::new(InProcessWorkerPool::default()));
        let bytes = png_bytes(200, 200);

        let out1 = service
            .get_or_generate(FileId::new(1), 100, 100, [0; 8], std::io::Cursor::new(bytes.clone()))
            .await
            .unwrap();
        let decoded = image::load_from_memory_with_format(&out1, image::ImageFormat::WebP).unwrap();
        // 100 rounds up to preset 128.
        assert!(decoded.width() <= 128 && decoded.height() <= 128);

        assert_eq!(service.generation_call_count(), 1);

        let out2 = service
            .get_or_generate(FileId::new(1), 100, 100, [0; 8], std::io::Cursor::new(bytes.clone()))
            .await
            .unwrap();
        assert_eq!(out1, out2);
        assert_eq!(service.generation_call_count(), 1, "second call must be served from cache");
    }

    /// A `WorkerPool` test double that counts calls and can be made
    /// artificially slow, so single-flight collapsing can be observed
    /// end-to-end through `PreviewService` (not just at the `PreviewCache`
    /// layer, which `cache.rs` already covers directly).
    struct CountingSlowPool {
        counter: Arc<AtomicUsize>,
        delay: Duration,
    }

    impl WorkerPool for CountingSlowPool {
        fn run_job(
            &self,
            req: JobRequest,
            _input: std::fs::File,
            _output: std::fs::File,
        ) -> Result<JobResponse, PreviewError> {
            self.counter.fetch_add(1, Ordering::SeqCst);
            std::thread::sleep(self.delay);
            Ok(JobResponse {
                job_id: req.job_id,
                result: JobResult::Ok { bytes_written: 4 },
            })
        }
    }

    #[tokio::test]
    async fn single_flight_through_the_full_service() {
        let counter = Arc::new(AtomicUsize::new(0));
        let pool = Arc::new(CountingSlowPool {
            counter: counter.clone(),
            delay: Duration::from_millis(80),
        });
        let service = Arc::new(PreviewService::new(PreviewConfig::default(), pool));
        let bytes = png_bytes(64, 64);

        let mut handles = Vec::new();
        for _ in 0..8 {
            let service = service.clone();
            let bytes = bytes.clone();
            handles.push(tokio::spawn(async move {
                service
                    .get_or_generate(FileId::new(99), 64, 64, [7; 8], std::io::Cursor::new(bytes))
                    .await
            }));
        }
        for h in handles {
            h.await.unwrap().unwrap();
        }

        assert_eq!(counter.load(Ordering::SeqCst), 1);
        assert_eq!(service.generation_call_count(), 1);
    }
}

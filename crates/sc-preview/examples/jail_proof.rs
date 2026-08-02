//! Linux-only proof that the preview worker's jail is enforced by the
//! kernel, not by our own good intentions (`DESIGN-PREVIEW.md` §4.1–§4.2).
//!
//! Run inside the Rocky VM:
//!
//! ```text
//! cargo run -p sc-preview --example jail_proof
//! ```
//!
//! Same shape as `sc-vfs/examples/escape_proof.rs`, and for the same reason:
//! a security claim that cannot be executed is a comment. The body is
//! `cfg(target_os = "linux")`-gated rather than left to fail, because
//! `cargo test` compiles every example on every platform and a Windows dev
//! box must not fail the gate on a target this proof was never meant for.
//!
//! What is demonstrated, in order:
//!
//! 1. Landlock alone denies `open("/etc/passwd")` — proven in a child that
//!    installs *only* the Landlock layer, because with seccomp on top the
//!    kernel kills the process at `openat` before Landlock is consulted, and
//!    that would leave the inner layer untested.
//! 2. A real jailed worker generates a correct thumbnail through the
//!    `SCM_RIGHTS` path.
//! 3. That worker cannot `open("/etc/passwd")`.
//! 4. It cannot create a socket.
//! 5. It cannot `fork`.
//! 6. Each of those kills exactly one worker; the pool re-forks and the next
//!    thumbnail succeeds.
//! 7. `SIGKILL`ing a worker mid-job fails that job only, and the pool
//!    recovers.

#[cfg(not(target_os = "linux"))]
fn main() {
    eprintln!(
        "jail_proof only means anything on Linux: it proves that Landlock and seccomp, not our \
         code, are what confine the preview worker. Run it in the Rocky VM."
    );
}

#[cfg(target_os = "linux")]
fn main() {
    std::process::exit(linux::run());
}

#[cfg(target_os = "linux")]
mod linux {
    use std::io::{Read, Seek, SeekFrom, Write};
    use std::sync::Arc;

    use sc_preview::worker::jailed::{
        apply_landlock_deny_all, JailLimits, JailedPoolConfig, JailedWorkerPool, Probe,
    };
    use sc_preview::{JobKind, JobRequest, JobResult, WorkerPool};

    struct Report {
        checks: usize,
        passed: usize,
    }

    impl Report {
        fn check(&mut self, label: &str, ok: bool, detail: impl AsRef<str>) {
            self.checks += 1;
            if ok {
                self.passed += 1;
            }
            println!(
                "{:<44} {}  {}",
                label,
                if ok { "PASS" } else { "*** FAIL ***" },
                detail.as_ref()
            );
        }
    }

    /// A small PNG with a distinctive pixel, so "the thumbnail is correct"
    /// means more than "some bytes came back".
    fn source_png() -> Vec<u8> {
        let mut img = image::RgbImage::from_pixel(400, 300, image::Rgb([10, 20, 30]));
        for x in 0..40 {
            for y in 0..30 {
                img.put_pixel(x, y, image::Rgb([250, 0, 0]));
            }
        }
        let mut raw = Vec::new();
        image::DynamicImage::ImageRgb8(img)
            .write_to(&mut std::io::Cursor::new(&mut raw), image::ImageFormat::Png)
            .unwrap();
        raw
    }

    /// Run one image job through the pool and validate the produced WebP.
    fn thumbnail(pool: &JailedWorkerPool, job_id: u64) -> Result<(u32, u32, u64), String> {
        let raw = source_png();
        let mut input = tempfile::tempfile().map_err(|e| e.to_string())?;
        input.write_all(&raw).map_err(|e| e.to_string())?;
        input.seek(SeekFrom::Start(0)).map_err(|e| e.to_string())?;
        let output = tempfile::tempfile().map_err(|e| e.to_string())?;

        let resp = pool
            .run_job(
                JobRequest {
                    job_id,
                    kind: JobKind::Image,
                    target_w: 128,
                    target_h: 128,
                },
                input,
                output.try_clone().map_err(|e| e.to_string())?,
            )
            .map_err(|e| e.to_string())?;

        let written = match resp.result {
            JobResult::Ok { bytes_written } => bytes_written,
            JobResult::Err { kind, reason } => return Err(format!("worker reported ({kind:?}): {reason}")),
        };

        let mut out = output;
        out.seek(SeekFrom::Start(0)).map_err(|e| e.to_string())?;
        let mut produced = Vec::new();
        out.read_to_end(&mut produced).map_err(|e| e.to_string())?;
        let decoded = image::load_from_memory_with_format(&produced, image::ImageFormat::WebP)
            .map_err(|e| format!("output is not decodable WebP: {e}"))?;
        Ok((decoded.width(), decoded.height(), written))
    }

    /// Fork a child that installs *only* the Landlock layer and then tries to
    /// open a file by path. Exit code carries the verdict.
    ///
    /// This is what proves the filesystem layer independently: in the real
    /// worker, seccomp kills `openat` before the kernel ever asks Landlock.
    fn landlock_only_open_etc_passwd() -> String {
        // SAFETY: single-threaded at this point in the proof, and the child
        // branch does nothing but syscalls and `_exit`.
        let pid = unsafe { libc::fork() };
        if pid == 0 {
            if let Err(e) = apply_landlock_deny_all() {
                eprintln!("      landlock child: {e}");
                // SAFETY: immediate exit.
                unsafe { libc::_exit(3) }
            }
            // SAFETY: open(2) with a static NUL-terminated path.
            let fd = unsafe { libc::open(c"/etc/passwd".as_ptr(), libc::O_RDONLY) };
            let code = if fd < 0 {
                let e = std::io::Error::last_os_error();
                eprintln!("      landlock child: open(/etc/passwd) -> {e}");
                0
            } else {
                eprintln!("      landlock child: open(/etc/passwd) SUCCEEDED (fd {fd})");
                1
            };
            // SAFETY: immediate exit.
            unsafe { libc::_exit(code) }
        }

        let mut status: libc::c_int = 0;
        // SAFETY: waiting on our own child.
        unsafe { libc::waitpid(pid, &mut status, 0) };
        if libc::WIFEXITED(status) {
            match libc::WEXITSTATUS(status) {
                0 => "denied".into(),
                1 => "OPENED".into(),
                n => format!("child error (exit {n})"),
            }
        } else {
            format!("child died with raw status {status}")
        }
    }

    pub fn run() -> i32 {
        let mut r = Report { checks: 0, passed: 0 };

        println!("kernel: {}", std::fs::read_to_string("/proc/sys/kernel/osrelease").unwrap_or_default().trim());
        println!(
            "lsm:    {}",
            std::fs::read_to_string("/sys/kernel/security/lsm").unwrap_or_default().trim()
        );
        println!();

        // --- 1. Landlock, on its own -------------------------------------
        let verdict = landlock_only_open_etc_passwd();
        r.check(
            "landlock-only: open(/etc/passwd)",
            verdict == "denied",
            format!("({verdict})"),
        );

        // --- the real pool ------------------------------------------------
        // One worker, so that every death below is unambiguous about which
        // process was killed and which one served the next job.
        let cfg = JailedPoolConfig {
            workers: 1,
            limits: JailLimits::default(),
            ..Default::default()
        };
        let pool = match JailedWorkerPool::new(cfg) {
            Ok(p) => Arc::new(p),
            Err(e) => {
                println!("could not fork a jailed worker pool at all: {e}");
                return 1;
            }
        };
        println!("forked worker pids: {:?}\n", pool.worker_pids());

        // --- 2. a thumbnail actually comes out the other side -------------
        match thumbnail(&pool, 1) {
            Ok((w, h, n)) => r.check(
                "jailed worker: generates a thumbnail",
                w == 128 && h == 96 && n > 0,
                format!("({w}x{h} WebP, {n} bytes; 400x300 -> 4:3 preserved)"),
            ),
            Err(e) => r.check("jailed worker: generates a thumbnail", false, e),
        }

        // --- 3/4/5. the three forbidden operations ------------------------
        for (label, probe) in [
            ("jailed worker: open(/etc/passwd)", Probe::OpenEtcPasswd),
            ("jailed worker: socket(AF_INET)", Probe::CreateSocket),
            ("jailed worker: fork()", Probe::Fork),
        ] {
            // Each probe costs a worker, so re-arm the slot first and learn
            // the pid of the process that is about to be killed.
            let _ = pool.probe(Probe::Ping);
            let pid_before = pool.worker_pids()[0];
            let outcome = pool.probe(probe);
            let (ok, detail) = match outcome {
                // The worker never came back: seccomp killed it. This is the
                // expected, and strongest, answer.
                Err(e) => {
                    let s = e.to_string();
                    (s.contains("signal 31") || s.contains("SIGSYS"), s)
                }
                // It survived and the kernel merely refused. Still contained,
                // but it means seccomp did not catch this one.
                Ok(sc_preview::worker::jailed::ProbeOutcome::Denied(d)) => {
                    (true, format!("survived, kernel refused: {d}"))
                }
                Ok(sc_preview::worker::jailed::ProbeOutcome::Succeeded(d)) => {
                    (false, format!("JAIL BREACHED: {d}"))
                }
            };
            r.check(label, ok, format!("(worker pid {pid_before}) {detail}"));
        }

        // --- 6. the pool re-forks and keeps serving -----------------------
        match thumbnail(&pool, 2) {
            Ok((w, h, n)) => r.check(
                "pool recovers after three worker kills",
                w == 128 && h == 96 && n > 0,
                format!("(new pid {:?}, {w}x{h} WebP, {n} bytes)", pool.worker_pids()),
            ),
            Err(e) => r.check("pool recovers after three worker kills", false, e),
        }

        // --- 7. SIGKILL mid-job ------------------------------------------
        // Re-arm the slot so there is definitely a live worker, and learn its
        // pid before handing it a long job.
        let _ = pool.probe(Probe::Ping);
        let victim = pool.worker_pids()[0];
        if victim <= 0 {
            // Never let a `-1` reach `kill(2)`: that is "signal every process
            // you are permitted to signal", which on a test box means the
            // session running this proof.
            r.check(
                "SIGKILL mid-job fails only that job",
                false,
                "no live worker to kill; skipped rather than calling kill(2) with a \
                 non-positive pid",
            );
        } else {
            let spinner = {
                let pool = Arc::clone(&pool);
                std::thread::spawn(move || pool.probe(Probe::Spin { millis: 5000 }))
            };
            std::thread::sleep(std::time::Duration::from_millis(400));
            // SAFETY: `victim` is a positive pid of a process we forked.
            let killed = unsafe { libc::kill(victim, libc::SIGKILL) };
            let spin_result = spinner.join().expect("prober thread panicked");
            r.check(
                "SIGKILL mid-job fails only that job",
                killed == 0 && spin_result.is_err(),
                match &spin_result {
                    Err(e) => format!("(killed pid {victim}) {e}"),
                    Ok(o) => format!("job survived the kill: {o:?}"),
                },
            );
        }

        match thumbnail(&pool, 3) {
            Ok((w, h, n)) => r.check(
                "pool recovers after mid-job SIGKILL",
                w == 128 && h == 96 && n > 0,
                format!("(new pid {:?}, {w}x{h} WebP, {n} bytes)", pool.worker_pids()),
            ),
            Err(e) => r.check("pool recovers after mid-job SIGKILL", false, e),
        }

        // --- 8. the production shape --------------------------------------
        // `sc-server` forks this pool from inside a multi-threaded tokio
        // runtime and reaches it through `PreviewService`, so prove that
        // exact arrangement rather than only the single-threaded one above.
        // This is also the case the module docs flag as the residual risk of
        // `fork` without `exec`.
        let rt = tokio::runtime::Builder::new_multi_thread()
            .worker_threads(4)
            .enable_all()
            .build()
            .expect("tokio runtime");
        let via_service = rt.block_on(async {
            let pool = JailedWorkerPool::new(JailedPoolConfig {
                workers: 2,
                ..Default::default()
            })
            .map_err(|e| e.to_string())?;
            let svc = sc_preview::PreviewService::new(
                sc_preview::PreviewConfig::default(),
                Arc::new(pool),
            );
            let raw = source_png();
            let bytes = svc
                .get_or_generate(
                    sc_vfs::ids::FileId(1),
                    128,
                    128,
                    [0u8; 8],
                    std::io::Cursor::new(raw),
                )
                .await
                .map_err(|e| e.to_string())?;
            let decoded = image::load_from_memory_with_format(&bytes, image::ImageFormat::WebP)
                .map_err(|e| e.to_string())?;
            Ok::<_, String>((decoded.width(), decoded.height(), bytes.len()))
        });
        match via_service {
            Ok((w, h, n)) => r.check(
                "PreviewService + jail, from a tokio runtime",
                w == 128 && h == 96 && n > 0,
                format!("({w}x{h} WebP, {n} bytes)"),
            ),
            Err(e) => r.check("PreviewService + jail, from a tokio runtime", false, e),
        }

        println!("\n--- {}/{} checks passed ---", r.passed, r.checks);
        i32::from(r.passed != r.checks)
    }
}

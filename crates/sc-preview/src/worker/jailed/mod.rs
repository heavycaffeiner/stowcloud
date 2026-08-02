//! The real worker jail (`DESIGN-PREVIEW.md` §4.1–§4.2), Linux-only.
//!
//! `#[cfg(target_os = "linux")]`. On Windows this module does not exist and
//! [`super::InProcessWorkerPool`] is the backend; the Windows build is
//! type-checked against Linux via `cargo check --target
//! x86_64-unknown-linux-musl`, but the only place any of this *means*
//! anything is a real kernel. `examples/jail_proof.rs` is the executable
//! form of that claim.
//!
//! # The structure
//!
//! [`JailedWorkerPool::new`] forks one or more worker children at
//! construction time. Each child is connected to the parent by an
//! `AF_UNIX`/`SOCK_SEQPACKET` socketpair and, before it looks at a single
//! byte of user input, walls itself in:
//!
//! 1. every inherited descriptor except the job socket and stdio is closed
//!    (`close_range`), so the child cannot reach the server's listening
//!    sockets, its SQLite files, or its epoll instance;
//! 2. Landlock with an **empty** ruleset — every filesystem access right the
//!    running kernel knows about is handled and none is granted, so there is
//!    no path the worker can open, anywhere;
//! 3. `RLIMIT_AS` / `RLIMIT_CPU` / `RLIMIT_NOFILE` / `RLIMIT_NPROC` /
//!    `RLIMIT_CORE`;
//! 4. a seccomp-BPF allow-list of twenty-two syscalls, everything else
//!    `SECCOMP_RET_KILL_PROCESS` ([`seccomp`]).
//!
//! Jobs arrive as a postcard-encoded message plus **two file descriptors
//! passed with `SCM_RIGHTS`**: input and output. The worker is never told a
//! path. That is not a policy the worker enforces, it is a fact about what
//! it was handed — path traversal in a decoder has nothing to traverse,
//! because `openat` is not on the allow-list and Landlock would refuse it
//! anyway.
//!
//! # Worker death is a normal event
//!
//! A seccomp kill (`SIGSYS`), an OOM against `RLIMIT_AS`, a segfault, or the
//! CPU limit firing all look the same from the parent: the response
//! `recvmsg` comes back empty or with `ECONNRESET`. The parent reaps the
//! child, fails **that one job** with [`PreviewError::Worker`], and forks a
//! replacement on the next job that lands on the same slot. A crafted input
//! that kills a decoder costs one thumbnail, not the server.
//!
//! # Known limitation: `fork` without `exec`
//!
//! The children are plain `fork(2)`s of the parent, as §4.2 describes — no
//! `execve`, which keeps the pool self-contained (no second binary to ship,
//! no argv protocol) and makes the jail testable from an `example`. The cost
//! is the classic one: between `fork` and the child's first allocation, only
//! async-signal-safe work is strictly legal, and the child *does* allocate
//! (it decodes images). If another thread in the parent held the allocator
//! lock at the instant of the fork, the child can deadlock.
//!
//! This is mitigated, not eliminated: construct the pool **early**, before
//! the process is busy — `sc-server` does so during `AppState::build`. A
//! hardened variant would re-`exec` a dedicated `sc-preview-worker` binary
//! and pass the socket as fd 3; that is a strictly better shape for
//! production and is deliberately left as follow-up rather than pretended
//! away. Note that the deadlock window is a liveness risk in the parent's
//! *child*, not a containment hole: a wedged worker is detected by the same
//! path as a dead one once a job times out at the caller.

pub mod seccomp;

use std::io;
use std::os::fd::{AsFd, AsRawFd, BorrowedFd, FromRawFd, IntoRawFd, OwnedFd, RawFd};
use std::sync::atomic::{AtomicI32, AtomicUsize, Ordering};

use landlock::{Access, AccessFs, Ruleset, RulesetAttr, ABI};
use parking_lot::Mutex;
use rustix::process::{setrlimit, Resource, Rlimit};
use serde::{Deserialize, Serialize};

use crate::decode::DecodeLimits;
use crate::error::{NegativeReason, PreviewError};
use crate::pipeline;

use super::{JobKind, JobRequest, JobResponse, JobResult, WorkerPool};

/// Largest wire message either direction. Both `WireRequest` and
/// `WireResponse` are a handful of scalars plus, at worst, a decoder error
/// string; `SOCK_SEQPACKET` preserves message boundaries, so this only has
/// to be an upper bound, never a framing parameter.
const MAX_MSG: usize = 8192;

/// Space for a `SCM_RIGHTS` control message carrying two descriptors.
/// `CMSG_SPACE(2 * sizeof(int))` is 24 bytes on every Linux ABI we build
/// for; 64 bytes of `u64`-aligned storage covers it with room to spare, and
/// the `u64` element type is what gives the buffer the alignment
/// `struct cmsghdr` requires.
const CMSG_WORDS: usize = 8;

/// `close_range(2)`. Spelled out rather than taken from `libc` because the
/// constant's presence varies by `libc` version and target libc, and 436 is
/// the number on every architecture that has the call (it was added to all
/// of them in the same cycle).
const SYS_CLOSE_RANGE: libc::c_long = 436;

/// Upper bound for the pre-`close_range` fallback sweep. Higher than any
/// plausible descriptor the parent holds, low enough that the loop is
/// instant.
const FD_SWEEP_MAX: RawFd = 65536;

// ---------------------------------------------------------------------------
// Configuration
// ---------------------------------------------------------------------------

/// Resource limits applied to the worker process (`DESIGN-PREVIEW.md` §4.2,
/// step ②).
#[derive(Debug, Clone)]
pub struct JailLimits {
    pub max_address_space_bytes: u64,
    pub max_cpu_seconds: u64,
    pub max_open_files: u64,
    pub max_child_processes: u64,
}

impl Default for JailLimits {
    fn default() -> Self {
        Self {
            max_address_space_bytes: 512 * 1024 * 1024,
            max_cpu_seconds: 10,
            max_open_files: 16,
            max_child_processes: 0,
        }
    }
}

#[derive(Debug, Clone)]
pub struct JailedPoolConfig {
    /// Number of worker children. Each is a `fork` of the parent, so they
    /// are cheap (copy-on-write) but not free; the useful ceiling is the
    /// caller's own generation-concurrency cap
    /// (`PreviewConfig::max_concurrent_generations`).
    pub workers: usize,
    pub limits: JailLimits,
    /// Decode limits handed to the pipeline *inside* the worker. These are
    /// belt-and-braces relative to `RLIMIT_AS`: the rlimit is the hard stop,
    /// this is the graceful one that reports a job-level error instead of
    /// costing a worker.
    pub decode: DecodeLimits,
}

impl Default for JailedPoolConfig {
    fn default() -> Self {
        Self {
            workers: (std::thread::available_parallelism()
                .map(|n| n.get())
                .unwrap_or(4)
                / 2)
            .max(1),
            limits: JailLimits::default(),
            decode: DecodeLimits::default(),
        }
    }
}

// ---------------------------------------------------------------------------
// Wire protocol (private to this module)
// ---------------------------------------------------------------------------

/// What actually goes over the socket. Kept private so that adding a
/// self-test probe does not widen the public [`JobRequest`] that `sc-http`
/// and the in-process pool share.
#[derive(Serialize, Deserialize)]
enum WireRequest {
    Job(JobRequest),
    Probe(Probe),
}

#[derive(Serialize, Deserialize)]
enum WireResponse {
    Job(JobResponse),
    Probe(ProbeOutcome),
}

/// A forbidden operation for the worker to *attempt*, so that the jail can
/// be demonstrated rather than asserted.
///
/// This grants nothing. The probe runs inside the jail, after every
/// restriction is in place; its only possible outcomes are "the kernel said
/// no" and "the kernel killed me". It exists because a security claim that
/// cannot be executed is a comment.
///
/// See `examples/jail_proof.rs`.
#[derive(Debug, Clone, Copy, Serialize, Deserialize)]
pub enum Probe {
    /// `open("/etc/passwd", O_RDONLY)`.
    OpenEtcPasswd,
    /// `socket(AF_INET, SOCK_STREAM, 0)`.
    CreateSocket,
    /// `fork()`.
    Fork,
    /// Burn CPU in userspace for up to `millis`, so the parent can kill the
    /// worker mid-job.
    Spin { millis: u64 },
    /// Do nothing. Confirms the worker is alive and the transport works.
    Ping,
}

/// What the worker managed to do. `Denied` is the good outcome; `Succeeded`
/// means the jail has a hole. Note that for the syscalls this jail kills
/// outright, the worker never gets to send *either* — the parent sees the
/// death instead, which is the strongest of the three outcomes.
#[derive(Debug, Clone, Serialize, Deserialize)]
pub enum ProbeOutcome {
    Denied(String),
    Succeeded(String),
}

// ---------------------------------------------------------------------------
// The jail
// ---------------------------------------------------------------------------

/// Close every descriptor the child inherited except stdio and `keep`.
///
/// This runs *before* Landlock and seccomp, and it matters as much as
/// either: the parent is a file server, so its descriptor table contains
/// listening sockets, open share roots, and SQLite databases. `RLIMIT_NOFILE`
/// caps how many *new* descriptors the worker may obtain; it does nothing
/// about the ones it was born holding.
///
/// `keep` is renumbered to fd 3 first so the survivors are a contiguous
/// `0..=3` and a single `close_range` call finishes the job.
fn seal_fd_table(keep: RawFd) -> Result<RawFd, PreviewError> {
    let kept = if keep == 3 {
        3
    } else {
        // SAFETY: dup2 onto a descriptor number we are about to own; any
        // previous occupant of fd 3 is one of the descriptors we are
        // deliberately discarding.
        let rc = unsafe { libc::dup2(keep, 3) };
        if rc < 0 {
            return Err(PreviewError::Worker(format!(
                "dup2(job socket -> 3) failed: {}",
                io::Error::last_os_error()
            )));
        }
        3
    };

    // SAFETY: close_range with no flags; closing descriptors this process
    // owns and will never use again.
    let rc = unsafe {
        libc::syscall(
            SYS_CLOSE_RANGE,
            4 as libc::c_long,
            u32::MAX as libc::c_long,
            0 as libc::c_long,
        )
    };
    if rc != 0 {
        // Pre-5.9 kernel. Fall back to the portable sweep.
        for fd in 4..FD_SWEEP_MAX {
            // SAFETY: closing a descriptor we may or may not own; EBADF is
            // the expected and harmless result for the ones we do not.
            unsafe { libc::close(fd) };
        }
    }

    Ok(kept)
}

/// Landlock with an empty ruleset: handle every filesystem access right the
/// running kernel supports, grant none of them (`DESIGN-PREVIEW.md` §4.2,
/// step ①). No `add_rule` call is missing here — the absence *is* the
/// policy. The worker's input and output arrive as open descriptors, so it
/// needs no path at all.
///
/// Exposed separately from [`enter_jail`] so the proof can demonstrate this
/// layer on its own; with seccomp installed, `openat` is killed before
/// Landlock is ever consulted, which proves the outer layer but says nothing
/// about the inner one.
pub fn apply_landlock_deny_all() -> Result<(), PreviewError> {
    let abi = ABI::V4;
    let status = Ruleset::default()
        .handle_access(AccessFs::from_all(abi))
        .map_err(|e| PreviewError::Worker(format!("landlock handle_access failed: {e}")))?
        .create()
        .map_err(|e| PreviewError::Worker(format!("landlock create failed: {e}")))?
        .restrict_self()
        .map_err(|e| PreviewError::Worker(format!("landlock restrict_self failed: {e}")))?;

    // Best-effort compatibility is the crate default: on a kernel that only
    // implements an older Landlock ABI (Rocky 9's 5.14 is ABI v1) the
    // unsupported access rights are dropped rather than failing the call.
    // Say so out loud rather than letting a downgrade pass silently.
    if status.ruleset != landlock::RulesetStatus::FullyEnforced {
        tracing::warn!(
            status = ?status.ruleset,
            "landlock ruleset not fully enforced on this kernel; the access rights it does \
             implement are still denied, and seccomp still blocks openat outright"
        );
    }
    Ok(())
}

/// `RLIMIT_*` (`DESIGN-PREVIEW.md` §4.2, step ②).
pub fn apply_rlimits(limits: &JailLimits) -> Result<(), PreviewError> {
    set_rlimit(Resource::As, limits.max_address_space_bytes)?;
    set_rlimit(Resource::Cpu, limits.max_cpu_seconds)?;
    set_rlimit(Resource::Nofile, limits.max_open_files)?;
    set_rlimit(Resource::Nproc, limits.max_child_processes)?;
    set_rlimit(Resource::Core, 0)?; // no core dumps: they can leak user data
    Ok(())
}

fn set_rlimit(resource: Resource, value: u64) -> Result<(), PreviewError> {
    setrlimit(
        resource,
        Rlimit {
            current: Some(value),
            maximum: Some(value),
        },
    )
    .map_err(|e| PreviewError::Worker(format!("setrlimit({resource:?}, {value}) failed: {e}")))
}

/// Enter the jail: Landlock, then rlimits, then seccomp — in that order,
/// because each step needs syscalls the next one forbids.
///
/// Called once, in the worker child, after its descriptor table has been
/// sealed and before it touches any job input. Irreversible.
pub fn enter_jail(limits: &JailLimits) -> Result<(), PreviewError> {
    apply_landlock_deny_all()?;
    apply_rlimits(limits)?;
    seccomp::install()?; // must be last
    Ok(())
}

// ---------------------------------------------------------------------------
// SCM_RIGHTS transport
// ---------------------------------------------------------------------------

fn last_err() -> io::Error {
    io::Error::last_os_error()
}

/// Close descriptors received over `SCM_RIGHTS`. See [`recv_msg`] for why
/// this is done by hand instead of with `OwnedFd`.
fn close_all(fds: &mut Vec<RawFd>) {
    for fd in fds.drain(..) {
        // SAFETY: each of these was created by the kernel for this process
        // when the control message was received, and is referenced nowhere
        // else.
        unsafe { libc::close(fd) };
    }
}

/// `sendmsg` one datagram, optionally carrying descriptors as `SCM_RIGHTS`.
fn send_msg(sock: BorrowedFd<'_>, payload: &[u8], fds: &[RawFd]) -> io::Result<()> {
    assert!(fds.len() <= 2, "protocol never passes more than two descriptors");

    let mut iov = libc::iovec {
        iov_base: payload.as_ptr() as *mut libc::c_void,
        iov_len: payload.len(),
    };
    let mut cmsg_buf = [0u64; CMSG_WORDS];

    // SAFETY: `msghdr` is a plain C struct; all-zero is a valid starting
    // state and every field we care about is set explicitly below.
    let mut msg: libc::msghdr = unsafe { std::mem::zeroed() };
    msg.msg_iov = &mut iov;
    msg.msg_iovlen = 1;

    if !fds.is_empty() {
        let bytes = std::mem::size_of_val(fds) as libc::c_uint;
        // SAFETY: the control buffer is `u64`-aligned and larger than
        // CMSG_SPACE(8); CMSG_FIRSTHDR therefore returns a pointer into it,
        // and we write exactly `fds.len()` ints at CMSG_DATA.
        unsafe {
            msg.msg_control = cmsg_buf.as_mut_ptr() as *mut libc::c_void;
            msg.msg_controllen = libc::CMSG_SPACE(bytes) as _;
            let cmsg = libc::CMSG_FIRSTHDR(&msg);
            assert!(!cmsg.is_null());
            (*cmsg).cmsg_level = libc::SOL_SOCKET;
            (*cmsg).cmsg_type = libc::SCM_RIGHTS;
            (*cmsg).cmsg_len = libc::CMSG_LEN(bytes) as _;
            std::ptr::copy_nonoverlapping(fds.as_ptr(), libc::CMSG_DATA(cmsg) as *mut RawFd, fds.len());
        }
    }

    loop {
        // SAFETY: `msg` describes buffers that outlive the call.
        let n = unsafe { libc::sendmsg(sock.as_raw_fd(), &msg, libc::MSG_NOSIGNAL) };
        if n >= 0 {
            return Ok(());
        }
        let e = last_err();
        if e.kind() == io::ErrorKind::Interrupted {
            continue;
        }
        return Err(e);
    }
}

/// `recvmsg` one datagram. Returns the payload length and any descriptors
/// that came with it.
///
/// A zero-length return means the peer closed: on `SOCK_SEQPACKET` that is
/// how a dead worker (or a shut-down parent) shows up.
///
/// Descriptors come back as bare [`RawFd`]s, and every caller closes them
/// with `libc::close`, because the worker side of this call runs under the
/// seccomp filter and `OwnedFd`'s `Drop` is not usable there: in a build with
/// debug assertions, std's `OwnedFd::drop` first issues `fcntl(fd, F_GETFD)`
/// as a use-after-close check, and `fcntl` is not on the allow-list. That
/// cost a jailed worker its life on the very first job until it was tracked
/// down; keeping the allow-list identical to `DESIGN-PREVIEW.md` §4.2 and
/// managing these two descriptors by hand is the better trade.
fn recv_msg(sock: BorrowedFd<'_>, buf: &mut [u8], fds_out: &mut Vec<RawFd>) -> io::Result<usize> {
    let mut iov = libc::iovec {
        iov_base: buf.as_mut_ptr() as *mut libc::c_void,
        iov_len: buf.len(),
    };
    let mut cmsg_buf = [0u64; CMSG_WORDS];

    // SAFETY: see `send_msg`.
    let mut msg: libc::msghdr = unsafe { std::mem::zeroed() };
    msg.msg_iov = &mut iov;
    msg.msg_iovlen = 1;
    msg.msg_control = cmsg_buf.as_mut_ptr() as *mut libc::c_void;
    msg.msg_controllen = (CMSG_WORDS * 8) as _;

    let n = loop {
        // SAFETY: `msg` describes buffers that outlive the call.
        let n = unsafe { libc::recvmsg(sock.as_raw_fd(), &mut msg, libc::MSG_CMSG_CLOEXEC) };
        if n >= 0 {
            break n as usize;
        }
        let e = last_err();
        if e.kind() == io::ErrorKind::Interrupted {
            continue;
        }
        return Err(e);
    };

    // SAFETY: iterating the control buffer the kernel just filled in, using
    // the kernel's own macros to walk it.
    unsafe {
        let mut cmsg = libc::CMSG_FIRSTHDR(&msg);
        while !cmsg.is_null() {
            if (*cmsg).cmsg_level == libc::SOL_SOCKET && (*cmsg).cmsg_type == libc::SCM_RIGHTS {
                let data = libc::CMSG_DATA(cmsg);
                let payload_len = (*cmsg).cmsg_len as usize - (data as usize - cmsg as usize);
                let count = payload_len / std::mem::size_of::<RawFd>();
                for i in 0..count {
                    let mut fd: RawFd = -1;
                    std::ptr::copy_nonoverlapping((data as *const RawFd).add(i), &mut fd, 1);
                    fds_out.push(fd);
                }
            }
            cmsg = libc::CMSG_NXTHDR(&msg, cmsg);
        }
    }

    if msg.msg_flags & libc::MSG_CTRUNC != 0 {
        // Descriptors were dropped by the kernel for lack of control-buffer
        // space. Refuse the message rather than proceeding with a partial fd
        // set, and do not leak the part that did arrive.
        close_all(fds_out);
        return Err(io::Error::new(
            io::ErrorKind::InvalidData,
            "SCM_RIGHTS control message truncated",
        ));
    }

    Ok(n)
}

// ---------------------------------------------------------------------------
// Worker child
// ---------------------------------------------------------------------------

/// The worker's whole life. Never returns — exits via `_exit` so that none
/// of the parent's `atexit` handlers, destructors, or buffered writers run
/// in a process that only borrowed the parent's address space.
fn worker_main(sock: OwnedFd, cfg: &JailedPoolConfig) -> ! {
    let sock = match seal_fd_table(sock.as_raw_fd()) {
        Ok(fd) => {
            // `sock` may name a descriptor `seal_fd_table` just closed;
            // forget it and take ownership of the renumbered one instead.
            let _ = sock.into_raw_fd();
            // SAFETY: `seal_fd_table` guarantees this descriptor is open and
            // is the only surviving reference to the job socket.
            unsafe { OwnedFd::from_raw_fd(fd) }
        }
        Err(e) => {
            eprintln!("sc-preview worker: {e}");
            unsafe { libc::_exit(101) }
        }
    };

    if let Err(e) = enter_jail(&cfg.limits) {
        eprintln!("sc-preview worker: refusing to run unjailed: {e}");
        // SAFETY: immediate process exit.
        unsafe { libc::_exit(102) }
    }

    // --- everything below this line runs under the seccomp allow-list ---

    let mut buf = [0u8; MAX_MSG];
    let mut fds: Vec<RawFd> = Vec::new();
    loop {
        close_all(&mut fds);
        let n = match recv_msg(sock.as_fd(), &mut buf, &mut fds) {
            Ok(0) => break, // parent hung up: shut down cleanly
            Ok(n) => n,
            Err(_) => break,
        };

        let req: WireRequest = match postcard::from_bytes(&buf[..n]) {
            Ok(r) => r,
            Err(_) => break,
        };

        let resp = match req {
            WireRequest::Job(job) => WireResponse::Job(run_job_in_worker(job, &fds, &cfg.decode)),
            WireRequest::Probe(p) => WireResponse::Probe(run_probe(p)),
        };
        close_all(&mut fds);

        let Ok(bytes) = postcard::to_allocvec(&resp) else { break };
        if send_msg(sock.as_fd(), &bytes, &[]).is_err() {
            break;
        }
    }

    // SAFETY: immediate process exit; nothing in this address space is ours
    // to unwind.
    unsafe { libc::_exit(0) }
}

fn run_job_in_worker(req: JobRequest, fds: &[RawFd], limits: &DecodeLimits) -> JobResponse {
    let fail = |kind: NegativeReason, reason: String| JobResponse {
        job_id: req.job_id,
        result: JobResult::Err { kind, reason },
    };

    if fds.len() != 2 {
        // A protocol invariant violated, not a decode problem — `WorkerError`
        // is the closest existing `NegativeReason`, and this should never
        // actually happen (the parent always sends exactly two descriptors).
        return fail(
            NegativeReason::WorkerError,
            format!("expected 2 descriptors via SCM_RIGHTS, got {}", fds.len()),
        );
    }
    if req.kind == JobKind::Video {
        // No jail can run ffmpeg without either relaxing this allow-list
        // (not on the table — this jail is proven on real hardware and
        // stays exactly as it is) or standing up a second, differently-
        // shaped jail: `execve` plus a Landlock rule scoped to the one
        // input file, which is a substantial new attack surface (ffmpeg's
        // own SSRF history via crafted playlists, a second kernel-verified
        // proof) and real future work rather than something to bolt on.
        // `NegativeReason::Unimplemented` — not `DecodeError` —
        // is what makes the refusal an honest, actionable answer instead of
        // a video file silently looking like a corrupt image.
        return fail(NegativeReason::Unimplemented, super::VIDEO_UNIMPLEMENTED_REASON.into());
    }

    let (input, output) = (fds[0], fds[1]);

    let raw = match read_all(input) {
        Ok(r) => r,
        Err(e) => return fail(NegativeReason::WorkerError, format!("reading job input: {e}")),
    };

    match pipeline::generate_preview_bytes(&raw, req.target_w, req.target_h, limits) {
        Ok(bytes) => match write_all(output, &bytes) {
            Ok(()) => JobResponse {
                job_id: req.job_id,
                result: JobResult::Ok {
                    bytes_written: bytes.len() as u64,
                },
            },
            Err(e) => fail(NegativeReason::WorkerError, format!("writing job output: {e}")),
        },
        Err(e) => fail(e.classify(), e.to_string()),
    }
}

/// Read a descriptor to EOF with a hand-rolled `read(2)` loop.
///
/// Deliberately not `File::read_to_end`: std's `File` specialization sizes
/// the buffer from `File::metadata`, which issues `statx` on a modern
/// kernel, which is not on the seccomp allow-list and would therefore kill
/// the worker. `pread64` from offset 0 upward also keeps the caller's file
/// offset out of the picture entirely.
fn read_all(fd: RawFd) -> io::Result<Vec<u8>> {
    let mut out = Vec::with_capacity(64 * 1024);
    let mut off: u64 = 0;
    let mut chunk = [0u8; 64 * 1024];
    loop {
        // SAFETY: `chunk` is a live buffer of exactly `chunk.len()` bytes.
        // `pread` on a 64-bit target is the `pread64` syscall, which is what
        // the seccomp allow-list names.
        let n = unsafe {
            libc::pread(
                fd,
                chunk.as_mut_ptr() as *mut libc::c_void,
                chunk.len(),
                off as libc::off_t,
            )
        };
        match n {
            0 => return Ok(out),
            n if n > 0 => {
                out.extend_from_slice(&chunk[..n as usize]);
                off += n as u64;
            }
            _ => {
                let e = last_err();
                if e.kind() == io::ErrorKind::Interrupted {
                    continue;
                }
                return Err(e);
            }
        }
    }
}

fn write_all(fd: RawFd, mut bytes: &[u8]) -> io::Result<()> {
    while !bytes.is_empty() {
        // SAFETY: `bytes` is a live slice of exactly `bytes.len()` bytes.
        let n = unsafe { libc::write(fd, bytes.as_ptr() as *const libc::c_void, bytes.len()) };
        if n > 0 {
            bytes = &bytes[n as usize..];
            continue;
        }
        let e = last_err();
        if e.kind() == io::ErrorKind::Interrupted {
            continue;
        }
        return Err(e);
    }
    Ok(())
}

/// Attempt a forbidden operation and report what the kernel said. Reaching
/// the `Succeeded` arm at all means the jail is broken; for the syscalls
/// seccomp kills outright, this function never returns.
fn run_probe(p: Probe) -> ProbeOutcome {
    match p {
        Probe::Ping => ProbeOutcome::Denied("ping (nothing attempted)".into()),
        Probe::OpenEtcPasswd => {
            // SAFETY: open(2) with a static NUL-terminated path.
            let fd = unsafe { libc::open(c"/etc/passwd".as_ptr(), libc::O_RDONLY) };
            if fd < 0 {
                ProbeOutcome::Denied(format!("open(/etc/passwd) -> {}", last_err()))
            } else {
                // SAFETY: `fd` is a descriptor we just opened.
                unsafe { libc::close(fd) };
                ProbeOutcome::Succeeded("open(/etc/passwd) returned a descriptor".into())
            }
        }
        Probe::CreateSocket => {
            // SAFETY: socket(2) with constant arguments.
            let fd = unsafe { libc::socket(libc::AF_INET, libc::SOCK_STREAM, 0) };
            if fd < 0 {
                ProbeOutcome::Denied(format!("socket(AF_INET, SOCK_STREAM) -> {}", last_err()))
            } else {
                // SAFETY: `fd` is a descriptor we just created.
                unsafe { libc::close(fd) };
                ProbeOutcome::Succeeded("socket(AF_INET, SOCK_STREAM) returned a descriptor".into())
            }
        }
        Probe::Fork => {
            // SAFETY: fork(2); if it somehow succeeds the child exits
            // immediately without touching anything.
            let pid = unsafe { libc::fork() };
            match pid {
                0 => unsafe { libc::_exit(0) },
                p if p < 0 => ProbeOutcome::Denied(format!("fork() -> {}", last_err())),
                p => ProbeOutcome::Succeeded(format!("fork() returned child pid {p}")),
            }
        }
        Probe::Spin { millis } => {
            let start = std::time::Instant::now();
            let mut acc: u64 = 0;
            while start.elapsed().as_millis() < millis as u128 {
                acc = acc.wrapping_mul(6364136223846793005).wrapping_add(1);
                std::hint::black_box(acc);
            }
            ProbeOutcome::Denied(format!("spun for {millis}ms without being killed"))
        }
    }
}

// ---------------------------------------------------------------------------
// Parent-side pool
// ---------------------------------------------------------------------------

struct Worker {
    pid: libc::pid_t,
    /// `None` only between [`Worker::reap`] closing the socket and the
    /// struct being dropped.
    sock: Option<OwnedFd>,
}

impl Worker {
    /// `fork` a child, jail it, and return the parent's end of the socket.
    fn spawn(cfg: &JailedPoolConfig) -> Result<Self, PreviewError> {
        let mut sv: [libc::c_int; 2] = [-1, -1];
        // SAFETY: `sv` is a live two-element array, which is what
        // socketpair writes into.
        let rc = unsafe {
            libc::socketpair(
                libc::AF_UNIX,
                libc::SOCK_SEQPACKET | libc::SOCK_CLOEXEC,
                0,
                sv.as_mut_ptr(),
            )
        };
        if rc != 0 {
            return Err(PreviewError::Worker(format!(
                "socketpair(AF_UNIX, SOCK_SEQPACKET) failed: {}",
                last_err()
            )));
        }
        // SAFETY: both descriptors were just created by socketpair.
        let parent_end = unsafe { OwnedFd::from_raw_fd(sv[0]) };
        let child_end = unsafe { OwnedFd::from_raw_fd(sv[1]) };

        // SAFETY: see the module docs on `fork` without `exec`. The child
        // branch below does no locking of its own before it reaches
        // `worker_main`'s loop.
        let pid = unsafe { libc::fork() };
        match pid {
            -1 => Err(PreviewError::Worker(format!("fork() failed: {}", last_err()))),
            0 => {
                drop(parent_end);
                worker_main(child_end, cfg);
            }
            pid => {
                drop(child_end);
                Ok(Worker {
                    pid,
                    sock: Some(parent_end),
                })
            }
        }
    }

    fn sock(&self) -> BorrowedFd<'_> {
        self.sock.as_ref().expect("socket is live until reap").as_fd()
    }

    /// Reap the child, describing how it died. Called on every transport
    /// failure and on pool shutdown; the description is what surfaces in the
    /// caller's error, and distinguishing `SIGSYS` from `SIGKILL` from a
    /// clean exit is the difference between "seccomp caught something" and
    /// "we ran out of memory".
    ///
    /// Never blocks indefinitely: a worker wedged in the `fork`-without-`exec`
    /// allocator-lock window (see the module docs) would otherwise hang the
    /// caller here, so an unresponsive child gets `SIGKILL`.
    fn reap(&mut self) -> String {
        // Close our end first: a *healthy* worker sees `recvmsg` return 0 and
        // exits on its own, which is how pool shutdown should look.
        drop(self.sock.take());

        let mut status: libc::c_int = 0;
        for _ in 0..50 {
            // SAFETY: waitpid on our own child, with a live status slot.
            let rc = unsafe { libc::waitpid(self.pid, &mut status, libc::WNOHANG) };
            if rc == self.pid {
                return describe_exit(self.pid, status);
            }
            if rc < 0 {
                return format!("worker pid {} could not be reaped: {}", self.pid, last_err());
            }
            std::thread::sleep(std::time::Duration::from_millis(10));
        }

        // SAFETY: signalling our own child.
        unsafe { libc::kill(self.pid, libc::SIGKILL) };
        // SAFETY: as above; the child is now guaranteed to terminate.
        let rc = unsafe { libc::waitpid(self.pid, &mut status, 0) };
        if rc < 0 {
            return format!("worker pid {} could not be reaped: {}", self.pid, last_err());
        }
        format!("{} (unresponsive, force-killed)", describe_exit(self.pid, status))
    }
}

fn describe_exit(pid: libc::pid_t, status: libc::c_int) -> String {
    if libc::WIFSIGNALED(status) {
        let sig = libc::WTERMSIG(status);
        let note = match sig {
            libc::SIGSYS => " (SIGSYS: seccomp policy violation — a syscall outside the allow-list)",
            libc::SIGKILL => " (SIGKILL: RLIMIT_AS/OOM, external kill, or RLIMIT_CPU hard limit)",
            libc::SIGXCPU => " (SIGXCPU: RLIMIT_CPU soft limit)",
            libc::SIGSEGV => " (SIGSEGV: memory fault inside the decoder)",
            libc::SIGABRT => " (SIGABRT: panic or abort inside the worker)",
            _ => "",
        };
        format!("worker pid {pid} killed by signal {sig}{note}")
    } else if libc::WIFEXITED(status) {
        format!("worker pid {pid} exited with status {}", libc::WEXITSTATUS(status))
    } else {
        format!("worker pid {pid} terminated with raw status {status}")
    }
}

/// One worker and its lock. The pid lives outside the mutex so that a
/// caller can identify (and, in the proof, kill) a worker that is currently
/// busy with somebody else's job.
struct Slot {
    worker: Mutex<Option<Worker>>,
    pid: AtomicI32,
}

/// The real pool: forked, jailed worker processes talking `SCM_RIGHTS` over
/// `SOCK_SEQPACKET`.
///
/// See the module docs. The short version: decoders run in a process with no
/// filesystem, no network, no ability to create processes, a 512 MiB address
/// space, a 10-second CPU budget, and twenty-two permitted syscalls.
pub struct JailedWorkerPool {
    cfg: JailedPoolConfig,
    slots: Vec<Slot>,
    next: AtomicUsize,
}

impl JailedWorkerPool {
    /// Fork the workers.
    ///
    /// Call this **early** in process startup — see the module docs on
    /// `fork` without `exec`. Returns `Err` if a worker could not be forked
    /// at all; note that a worker which forks but then fails to jail itself
    /// exits rather than serving jobs, so there is no path here that yields
    /// a running-but-unjailed worker.
    pub fn new(cfg: JailedPoolConfig) -> Result<Self, PreviewError> {
        let n = cfg.workers.max(1);
        let mut slots = Vec::with_capacity(n);
        for _ in 0..n {
            let w = Worker::spawn(&cfg)?;
            let pid = w.pid;
            slots.push(Slot {
                worker: Mutex::new(Some(w)),
                pid: AtomicI32::new(pid),
            });
        }
        tracing::info!(
            workers = n,
            "preview worker pool forked: landlock deny-all + rlimits + seccomp allow-list"
        );
        Ok(Self {
            cfg,
            slots,
            next: AtomicUsize::new(0),
        })
    }

    /// Live worker pids, in slot order. `-1` for a slot whose worker is
    /// currently dead and awaiting re-fork.
    pub fn worker_pids(&self) -> Vec<i32> {
        self.slots.iter().map(|s| s.pid.load(Ordering::Relaxed)).collect()
    }

    /// Attempt a forbidden operation inside a real jailed worker.
    ///
    /// `Ok(ProbeOutcome)` means the worker survived and reported what the
    /// kernel told it; `Err` means the kernel killed the worker, which for
    /// `OpenEtcPasswd`/`CreateSocket`/`Fork` is the expected — and stronger —
    /// answer. Either way the pool recovers, exactly as it does for a job
    /// that kills a worker.
    ///
    /// Self-test facility; see `examples/jail_proof.rs`.
    pub fn probe(&self, p: Probe) -> Result<ProbeOutcome, PreviewError> {
        match self.exchange(WireRequest::Probe(p), &[])? {
            WireResponse::Probe(o) => Ok(o),
            WireResponse::Job(_) => Err(PreviewError::Worker(
                "worker answered a probe with a job response".into(),
            )),
        }
    }

    /// Round-robin a slot, ensure it has a live worker, send, receive.
    ///
    /// Any transport failure is treated as "the worker is gone": it is
    /// reaped, the slot is emptied, and the error names how it died. The
    /// *next* job to land on this slot forks a replacement. That laziness is
    /// deliberate — re-forking on the failure path would mean forking while
    /// holding the slot lock in a caller that is already returning an error.
    fn exchange(&self, req: WireRequest, fds: &[RawFd]) -> Result<WireResponse, PreviewError> {
        let idx = self.next.fetch_add(1, Ordering::Relaxed) % self.slots.len();
        let slot = &self.slots[idx];
        let mut guard = slot.worker.lock();

        if guard.is_none() {
            let w = Worker::spawn(&self.cfg)?;
            slot.pid.store(w.pid, Ordering::Relaxed);
            *guard = Some(w);
        }

        let payload = postcard::to_allocvec(&req)
            .map_err(|e| PreviewError::Worker(format!("encoding job: {e}")))?;

        // Held as a raw descriptor rather than a borrow of `*guard`, so that
        // the failure path below can take the worker out of the slot without
        // fighting the borrow checker over it. The descriptor stays valid
        // for as long as we hold the slot lock.
        let sock = guard.as_ref().expect("just populated").sock().as_raw_fd();
        // SAFETY: the worker in this slot owns `sock` and cannot be replaced
        // while we hold the lock.
        let sock = unsafe { BorrowedFd::borrow_raw(sock) };

        let outcome = (|| -> io::Result<Vec<u8>> {
            send_msg(sock, &payload, fds)?;
            let mut buf = [0u8; MAX_MSG];
            // The worker never sends descriptors back; close anything that
            // arrives rather than leaking it.
            let mut got_fds: Vec<RawFd> = Vec::new();
            let n = recv_msg(sock, &mut buf, &mut got_fds);
            close_all(&mut got_fds);
            match n? {
                0 => Err(io::Error::new(
                    io::ErrorKind::UnexpectedEof,
                    "worker closed the socket",
                )),
                n => Ok(buf[..n].to_vec()),
            }
        })();

        match outcome {
            Ok(bytes) => postcard::from_bytes(&bytes)
                .map_err(|e| PreviewError::Worker(format!("decoding worker response: {e}"))),
            Err(e) => {
                // Any transport failure means the worker is gone: seccomp
                // kill, OOM, segfault, CPU limit, or an outright `kill -9`.
                // Reap it, empty the slot, and name the cause. The next job
                // on this slot forks a replacement.
                let how = guard.as_mut().map(|w| w.reap()).unwrap_or_default();
                *guard = None;
                slot.pid.store(-1, Ordering::Relaxed);
                Err(PreviewError::Worker(format!(
                    "preview worker failed the job ({e}); {how}"
                )))
            }
        }
    }
}

impl WorkerPool for JailedWorkerPool {
    fn run_job(
        &self,
        req: JobRequest,
        input: std::fs::File,
        output: std::fs::File,
    ) -> Result<JobResponse, PreviewError> {
        let job_id = req.job_id;
        // The worker receives these two descriptors and nothing else. No
        // path, no directory handle, no name.
        let fds = [input.as_raw_fd(), output.as_raw_fd()];
        match self.exchange(WireRequest::Job(req), &fds)? {
            WireResponse::Job(r) => Ok(r),
            WireResponse::Probe(_) => Err(PreviewError::Worker(format!(
                "worker answered job {job_id} with a probe response"
            ))),
        }
    }
}

impl Drop for JailedWorkerPool {
    fn drop(&mut self) {
        for slot in &self.slots {
            if let Some(mut w) = slot.worker.lock().take() {
                let _ = w.reap();
            }
        }
    }
}

#[cfg(test)]
mod tests {
    // Everything worth testing here needs a real Linux kernel with Landlock
    // and seccomp, a `fork`, and a socketpair -- which is what
    // `examples/jail_proof.rs` does, run in the Rocky VM. `seccomp`'s own
    // unit tests (BPF program shape) do run under plain `cargo test` on any
    // Linux target.
    //
    // The one thing worth pinning here without a kernel is that the wire
    // encoding round-trips, since a mismatch would look like a worker crash.
    use super::*;

    #[test]
    fn wire_messages_round_trip_through_postcard() {
        let req = WireRequest::Job(JobRequest {
            job_id: 42,
            kind: JobKind::Image,
            target_w: 256,
            target_h: 256,
        });
        let bytes = postcard::to_allocvec(&req).unwrap();
        assert!(bytes.len() < MAX_MSG);
        let back: WireRequest = postcard::from_bytes(&bytes).unwrap();
        match back {
            WireRequest::Job(j) => {
                assert_eq!(j.job_id, 42);
                assert_eq!(j.target_w, 256);
            }
            WireRequest::Probe(_) => panic!("wrong variant"),
        }

        let resp = WireResponse::Job(JobResponse {
            job_id: 42,
            result: JobResult::Err {
                kind: NegativeReason::DecodeError,
                reason: "x".repeat(512),
            },
        });
        let bytes = postcard::to_allocvec(&resp).unwrap();
        assert!(bytes.len() < MAX_MSG, "worst-case response must fit one datagram");
    }
}

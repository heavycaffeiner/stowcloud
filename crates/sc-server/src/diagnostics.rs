//! Startup self-diagnostics, in the spirit of and
//! print exactly what security/perf-relevant
//! kernel features are available, distinguish "not present" from "blocked
//! by policy" wherever the kernel lets us, and gate share registration on
//! filesystem type.

use std::net::IpAddr;
use std::path::{Path, PathBuf};

use crate::config::Config;

/// `openat2`'s specific failure mode matters for the log message:
/// `ENOSYS` is an old kernel (fine, fall back
/// silently-ish), `EPERM` is very likely Docker's default seccomp profile
/// denying an allow-listed syscall (a security *downgrade* that deserves a
/// loud message and an upgrade hint). `sc_vfs::detect_kernel_caps` folds
/// both into a single bool for its own (deliberately conservative) purposes,
/// so this is a separate, diagnostics-only probe.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum OpenAt2Status {
    Ok,
    /// Kernel predates `openat2` (< 5.6).
    UnsupportedKernel,
    /// Syscall exists but was denied — seccomp/Docker default profile.
    DeniedByPolicy,
    /// Non-Linux dev/test platform: `openat2` doesn't exist at all here.
    NotApplicable,
}

#[cfg(target_os = "linux")]
fn probe_openat2_detail() -> OpenAt2Status {
    use rustix::fs::{Mode, OFlags, ResolveFlags, CWD};
    use rustix::io::Errno;
    match rustix::fs::openat2(
        CWD,
        ".",
        OFlags::RDONLY | OFlags::DIRECTORY | OFlags::CLOEXEC,
        Mode::empty(),
        ResolveFlags::empty(),
    ) {
        Ok(_) => OpenAt2Status::Ok,
        Err(Errno::NOSYS) => OpenAt2Status::UnsupportedKernel,
        Err(Errno::PERM) => OpenAt2Status::DeniedByPolicy,
        Err(_) => OpenAt2Status::Ok,
    }
}

#[cfg(not(target_os = "linux"))]
fn probe_openat2_detail() -> OpenAt2Status {
    OpenAt2Status::NotApplicable
}

/// `fs.inotify.max_user_watches` (Linux only; host-global, per-UID, cannot
/// be raised from inside a container —).
#[cfg(target_os = "linux")]
fn inotify_max_watches() -> Option<u64> {
    std::fs::read_to_string("/proc/sys/fs/inotify/max_user_watches")
        .ok()?
        .trim()
        .parse()
        .ok()
}

#[cfg(not(target_os = "linux"))]
fn inotify_max_watches() -> Option<u64> {
    None
}

/// SELinux enforcement state ('s `:z`/`:Z` labeling
/// caveat). Read directly from selinuxfs rather than shelling out to
/// `getenforce` — one less subprocess dependency, and the same information
/// the binary would parse from its stdout anyway.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum SelinuxStatus {
    Enforcing,
    Permissive,
    /// Not compiled into the kernel, or selinuxfs isn't mounted.
    Disabled,
    NotApplicable,
}

#[cfg(target_os = "linux")]
fn probe_selinux() -> SelinuxStatus {
    // `/sys/fs/selinux` is selinuxfs's mount point; its absence means the
    // running kernel has no SELinux support to ask about.
    if !Path::new("/sys/fs/selinux").is_dir() {
        return SelinuxStatus::Disabled;
    }
    match std::fs::read_to_string("/sys/fs/selinux/enforce") {
        Ok(s) if s.trim() == "1" => SelinuxStatus::Enforcing,
        Ok(_) => SelinuxStatus::Permissive,
        Err(_) => SelinuxStatus::Disabled,
    }
}

#[cfg(not(target_os = "linux"))]
fn probe_selinux() -> SelinuxStatus {
    SelinuxStatus::NotApplicable
}

/// Filesystem type detection for the share-registration gate. Runs before any `ShareRoot` exists for the path —
/// refusing registration is the whole point — so it calls
/// `sc_vfs::FsType::from_statfs_magic` directly rather than going through a
/// share handle.
#[cfg(target_os = "linux")]
fn detect_fs_type(path: &Path) -> sc_vfs::FsType {
    use std::mem::MaybeUninit;
    use std::os::unix::ffi::OsStrExt;

    let cpath = match std::ffi::CString::new(path.as_os_str().as_bytes()) {
        Ok(c) => c,
        Err(_) => return sc_vfs::FsType::Other(0),
    };
    unsafe {
        let mut buf: MaybeUninit<libc::statfs> = MaybeUninit::uninit();
        if libc::statfs(cpath.as_ptr(), buf.as_mut_ptr()) != 0 {
            return sc_vfs::FsType::Other(0);
        }
        // `statfs.f_type` is `__fsword_t`, whose width and signedness differ
        // by libc and architecture — `i64` on glibc/x86_64, `u64` on musl,
        // `u32` on 32-bit. Clippy sees whichever target it was run against and
        // calls the cast redundant; on the others it is load-bearing.
        #[allow(clippy::unnecessary_cast)]
        let magic = buf.assume_init().f_type as u64;
        sc_vfs::FsType::from_statfs_magic(magic)
    }
}

#[cfg(not(target_os = "linux"))]
fn detect_fs_type(_path: &Path) -> sc_vfs::FsType {
    sc_vfs::FsType::Other(0)
}

/// Total/free bytes of the volume containing `path`. Best-effort — `None`
/// if the platform call fails.
#[cfg(unix)]
fn volume_stats(path: &Path) -> Option<(u64, u64)> {
    use std::mem::MaybeUninit;
    use std::os::unix::ffi::OsStrExt;
    let cpath = std::ffi::CString::new(path.as_os_str().as_bytes()).ok()?;
    unsafe {
        let mut buf: MaybeUninit<libc::statvfs> = MaybeUninit::uninit();
        if libc::statvfs(cpath.as_ptr(), buf.as_mut_ptr()) != 0 {
            return None;
        }
        let s = buf.assume_init();
        // Same story as `f_type` above: `fsblkcnt_t` and `f_frsize` are 64-bit
        // on 64-bit targets and 32-bit elsewhere. Widening first is also what
        // keeps the multiply from overflowing on a 32-bit build.
        #[allow(clippy::unnecessary_cast)]
        let total = s.f_blocks as u64 * s.f_frsize as u64;
        #[allow(clippy::unnecessary_cast)]
        let free = s.f_bavail as u64 * s.f_frsize as u64;
        Some((total, free))
    }
}

#[cfg(windows)]
fn volume_stats(path: &Path) -> Option<(u64, u64)> {
    use windows_sys::Win32::Storage::FileSystem::GetDiskFreeSpaceExW;
    let mut wide: Vec<u16> = path
        .as_os_str()
        .encode_wide()
        .chain(std::iter::once(0))
        .collect();
    let mut free_avail: u64 = 0;
    let mut total: u64 = 0;
    let mut total_free: u64 = 0;
    let ok = unsafe {
        GetDiskFreeSpaceExW(
            wide.as_mut_ptr(),
            &mut free_avail,
            &mut total,
            &mut total_free,
        )
    };
    if ok == 0 {
        None
    } else {
        Some((total, free_avail))
    }
}

#[cfg(windows)]
use std::os::windows::ffi::OsStrExt;

#[cfg(not(any(unix, windows)))]
fn volume_stats(_path: &Path) -> Option<(u64, u64)> {
    None
}

/// Spec-based DB-size-guard recommendation from
fn guard_recommendation(volume_total: Option<u64>) -> Option<(bool, u64)> {
    const GIB: u64 = 1024 * 1024 * 1024;
    let total = volume_total?;
    if total < 64 * GIB {
        Some((true, 2 * GIB))
    } else if total <= 256 * GIB {
        Some((true, 8 * GIB))
    } else {
        None // guard not necessary; keep it disabled
    }
}

pub struct FsProbe {
    pub name: String,
    pub host_path: PathBuf,
    pub fstype: sc_vfs::FsType,
    pub rejected: bool,
    /// `faccessat2`/`faccessat` real-access probe (item 45,
    /// `sc_core::probe_access`) — `None` on non-Linux or if `host_path`
    /// couldn't be `stat`ed (already covered by `rejected`/other checks).
    pub access: Option<sc_core::AccessProbe>,
}

pub struct Diagnostics {
    pub kernel_caps: sc_vfs::KernelCaps,
    pub openat2_detail: OpenAt2Status,
    pub inotify_max_watches: Option<u64>,
    pub selinux: SelinuxStatus,
    pub shares: Vec<FsProbe>,
    pub master_key_inside_data_dir: bool,
    pub master_key_generated: bool,
    pub db_bytes: u64,
    pub db_size_guard: bool,
    /// `cfg.db.max_bytes` at startup — config, so it cannot itself go stale
    /// the way a byte-count snapshot would. Paired with `db_size_guard` in
    /// [`Diagnostics::degraded_reasons`].
    pub max_bytes: u64,
    pub volume_total: Option<u64>,
    pub volume_free: Option<u64>,
    /// `cfg.db.min_free_bytes` at startup.
    /// always-on floor, which is independent of `db_size_guard` and has no
    /// off switch. Paired with [`FREE_BYTES`] in
    /// [`Diagnostics::degraded_reasons`].
    pub min_free_bytes: u64,
    pub size_guard_recommendation: Option<(bool, u64)>,
    pub smb_bind_result: Result<(), String>,
    /// No dedicated content origin configured, so user content is served from
    /// the app origin. Permitted but must be said
    /// out loud — it gives up the XSS isolation separation exists to provide.
    pub single_origin: bool,
    pub trusted_proxies: TrustedProxies,
    /// What `Config::resolve_compat_canonical_url` resolved to — reported
    /// unconditionally (computing it touches nothing beyond `cfg`'s own
    /// fields), but only ever *printed* when this binary was built with
    /// `feature = "compat-nc"` (`print`, below): a `--no-default-features`
    /// build has no compatibility layer to report on, and printing this
    /// anyway would be a confusing, meaningless line in that binary's logs.
    pub compat_canonical_url: crate::config::CompatCanonicalUrl,
}

/// What `trusted_proxies` actually parsed to.
///
/// requires this to be reported loudly: behind a proxy
/// with no CIDR list, `CF-Connecting-IP`/`X-Forwarded-For` are (correctly)
/// discarded, every request is attributed to the proxy, and the per-IP login
/// gate then throttles the whole user base as one — "legitimate users could
/// get blocked, so a proxy misconfiguration is loudly warned about at startup".
#[derive(Clone, Debug, Default)]
pub struct TrustedProxies {
    /// Entries that parsed into a usable CIDR.
    pub accepted: Vec<String>,
    /// Entries `sc_http::config::Cidr::parse` rejected. `App::build` drops
    /// these with `filter_map`, so without this they vanish silently — one
    /// typo in `SC_TRUSTED_PROXIES` and the proxy stops being trusted with no
    /// indication anywhere.
    pub rejected: Vec<String>,
    /// Entries with a `/0` prefix — "trust the entire internet to tell me who
    /// its clients are", which is the same as having no gate at all.
    pub wildcard: Vec<String>,
}

fn classify_trusted_proxies(entries: &[String]) -> TrustedProxies {
    let mut out = TrustedProxies::default();
    for e in entries {
        match sc_http::config::Cidr::parse(e) {
            Some(c) if c.prefix == 0 => {
                out.wildcard.push(e.clone());
                out.accepted.push(e.clone());
            }
            Some(_) => out.accepted.push(e.clone()),
            None => out.rejected.push(e.clone()),
        }
    }
    out
}

impl Diagnostics {
    pub fn any_share_rejected(&self) -> bool {
        self.shares.iter().any(|s| s.rejected)
    }

    /// Every non-fatal degraded condition currently in effect, named by a
    /// stable internal key — for logs and an *authenticated* status surface,
    /// never for `GET /api/health` directly. That route is reachable
    /// unauthenticated and forbids leaking anything past
    /// "the server exists" to an anonymous caller, and a reason string here
    /// is exactly the kind of configuration detail that rule exists to
    /// withhold (e.g. naming which share was rejected leaks a host path
    /// shape). [`Diagnostics::is_degraded`] is the bare signal that route
    /// may use instead.
    ///
    /// Startup-fatal conditions (the preview jail failing to sandbox, for
    /// instance — `app.rs`: "a failure here is fatal rather than a silent
    /// downgrade") never appear here: if one of those trips, `App::build`
    /// returns `Err` and the process exits before it ever binds a socket, so
    /// there is no running server left to ask. Everything below is a
    /// condition the server can be serving requests *while* it holds.
    ///
    /// `current_db_bytes` and `current_free_bytes` are the *live* figures.
    /// `self.db_bytes`/`self.volume_free` are one-time snapshots taken at
    /// startup — the database keeps growing and the volume keeps filling
    /// afterward — so a caller evaluating this at request time should pass
    /// the current numbers (`crate::bridge::DB_BYTES` and [`FREE_BYTES`]),
    /// not lean on the snapshots going stale under it.
    pub fn degraded_reasons(&self, current_db_bytes: u64, current_free_bytes: u64) -> Vec<&'static str> {
        let mut out = Vec::new();
        if self.any_share_rejected() {
            out.push("share_rejected");
        }
        if self.smb_bind_result.is_err() {
            out.push("smb_bind_failed");
        }
        if self.db_size_guard && current_db_bytes >= self.max_bytes {
            out.push("db_size_guard_tripped");
        }
        // Unconditional: §4.4's floor is "the baseline defense against SQLite
        // corruption and outright service death, and it cannot be turned
        // off" — so unlike the reason above it is not gated on a config flag.
        if current_free_bytes < self.min_free_bytes {
            out.push("db_free_space_low");
        }
        out
    }

    /// `true` once the process is up but running in a state an operator or
    /// uptime monitor should know about — the coarse, detail-free signal
    /// `GET /api/health` is allowed to answer with (e.g. `"degraded"`
    /// instead of `"ok"`) without becoming a fingerprinting surface. See
    /// [`Diagnostics::degraded_reasons`] for what feeds it and why the
    /// *reasons* themselves must stay behind auth.
    pub fn is_degraded(&self, current_db_bytes: u64, current_free_bytes: u64) -> bool {
        !self.degraded_reasons(current_db_bytes, current_free_bytes)
            .is_empty()
    }
}

/// The startup snapshot, kept so a request handler can consult it.
///
/// A global rather than a field threaded through `App::build`, matching the
/// existing precedent of [`crate::bridge::DB_BYTES`]: `run` is called before
/// the `App` exists (and by `gc`/`smb-sync`, which never build a router), and
/// widening `App::build`'s signature would churn every test that constructs
/// one for reasons unrelated to health.
///
/// Set once by [`crate::run_diagnostics_and_print`]. Absent means diagnostics
/// never ran, which is only true in tests — and `health` answers `"ok"` then,
/// because "we have nothing to report" is not the same claim as "something is
/// wrong".
pub static SNAPSHOT: std::sync::OnceLock<Diagnostics> = std::sync::OnceLock::new();

/// Free bytes on the volume holding `data_dir`, refreshed by
/// [`spawn_free_space_sampler`].
///
/// `u64::MAX` means "not sampled yet, or this platform has no
/// `volume_stats`". That value can never compare below any floor, which is
/// the behaviour we want: an unknown must not be read as "the disk is full"
/// and freeze metadata writes on a perfectly healthy server.
pub static FREE_BYTES: std::sync::atomic::AtomicU64 = std::sync::atomic::AtomicU64::new(u64::MAX);

/// How often the sampler re-reads free space and the database size. A
/// `statvfs` is one syscall and the DB size is two pragmas on a pooled
/// connection, so the interval is set by how long we are willing to keep
/// writing into a volume that just crossed the floor, not by cost.
const STORAGE_SAMPLE_INTERVAL: std::time::Duration = std::time::Duration::from_secs(30);

/// `true` if the startup snapshot exists and, against the *live* database
/// size and free space, reports something an operator should know about.
pub fn is_degraded_now() -> bool {
    use std::sync::atomic::Ordering;
    SNAPSHOT
        .get()
        .map(|d| {
            d.is_degraded(
                crate::bridge::DB_BYTES.load(Ordering::Relaxed),
                FREE_BYTES.load(Ordering::Relaxed),
            )
        })
        .unwrap_or(false)
}

/// Read free space and the database size once, publish them to [`FREE_BYTES`]
/// and [`crate::bridge::DB_BYTES`], and open or close the metadata store's
/// growth gate accordingly.
///
/// Split out from the loop below so a test can drive one tick without a
/// runtime or a 30-second wait.
pub fn sample_storage_once(
    data_dir: &Path,
    min_free_bytes: u64,
    meta: &sc_meta::MetaStore,
) -> u64 {
    let free = volume_stats(data_dir).map(|(_, f)| f).unwrap_or(u64::MAX);
    FREE_BYTES.store(free, std::sync::atomic::Ordering::Relaxed);

    // The other half of §4.4's guard. `DB_BYTES` was published at startup and
    // after `gc`, and nowhere else -- so `db_size_guard` compared a cap
    // against the size the process booted with, and a database that grew past
    // it while serving never tripped it. A failed read leaves the previous
    // value rather than storing 0, which would read as "empty database" and
    // silently un-trip a guard that is currently holding.
    if let Ok(bytes) = meta.size_bytes() {
        crate::bridge::DB_BYTES.store(bytes, std::sync::atomic::Ordering::Relaxed);
    }

    let block = free < min_free_bytes;
    if block != meta.writes_blocked() {
        if block {
            tracing::error!(
                free_bytes = free,
                min_free_bytes,
                "free space on the metadata volume fell below db.min_free_bytes: \
                 refusing new fileid/property allocations until it recovers \
. Browsing, downloads and uploads are \
                 unaffected."
            );
        } else {
            tracing::info!(
                free_bytes = free,
                min_free_bytes,
                "free space recovered above db.min_free_bytes: metadata writes resumed"
            );
        }
        meta.set_writes_blocked(block);
    }
    free
}

/// Spawn the periodic sampler.'s floor and cap are
/// only a floor and a cap if something keeps looking — `volume_free` and
/// `db_bytes` on the startup snapshot are single readings taken before the
/// server ever accepted a request.
pub fn spawn_storage_sampler(
    data_dir: PathBuf,
    min_free_bytes: u64,
    meta: std::sync::Arc<sc_meta::MetaStore>,
) -> tokio::task::JoinHandle<()> {
    tokio::spawn(async move {
        let mut tick = tokio::time::interval(STORAGE_SAMPLE_INTERVAL);
        loop {
            tick.tick().await;
            let dir = data_dir.clone();
            let m = meta.clone();
            // `statvfs` can block on a wedged NFS/network mount, and blocking
            // a runtime worker there would stall unrelated requests.
            let _ = tokio::task::spawn_blocking(move || {
                sample_storage_once(&dir, min_free_bytes, &m)
            })
            .await;
        }
    })
}

/// Run every probe. Never fails outright — a probe that can't run just
/// reports "unknown"/`None`, since diagnostics must not crash the process;
/// the one exception the design calls out (`strict_syscalls = true`) is left
/// to the caller to enforce after inspecting the result.
pub fn run(
    cfg: &Config,
    master_key: &crate::masterkey::MasterKeyResult,
    db_bytes: u64,
    smb_bind_result: Result<(), String>,
    single_origin: bool,
) -> Diagnostics {
    let kernel_caps = sc_vfs::detect_kernel_caps();
    let openat2_detail = probe_openat2_detail();
    let inotify_max_watches = inotify_max_watches();
    let selinux = probe_selinux();

    let shares = cfg
        .shares
        .iter()
        .map(|s| {
            let fstype = detect_fs_type(&s.host_path);
            FsProbe {
                name: s.name.clone(),
                host_path: s.host_path.clone(),
                rejected: fstype.is_rejected(),
                fstype,
                access: sc_core::probe_access(&s.host_path),
            }
        })
        .collect();

    let (volume_total, volume_free) = match volume_stats(&cfg.data_dir) {
        Some((t, f)) => (Some(t), Some(f)),
        None => (None, None),
    };
    let size_guard_recommendation = guard_recommendation(volume_total);

    Diagnostics {
        kernel_caps,
        openat2_detail,
        inotify_max_watches,
        selinux,
        shares,
        master_key_inside_data_dir: master_key.inside_data_dir,
        master_key_generated: master_key.generated,
        db_bytes,
        db_size_guard: cfg.db.size_guard,
        max_bytes: cfg.db.max_bytes,
        volume_total,
        volume_free,
        min_free_bytes: cfg.db.min_free_bytes,
        size_guard_recommendation,
        smb_bind_result,
        single_origin,
        trusted_proxies: classify_trusted_proxies(&cfg.trusted_proxies),
        compat_canonical_url: cfg.resolve_compat_canonical_url(),
    }
}

/// Print the probe results as the `[sc] ...` log lines the docs show.
pub fn print(d: &Diagnostics) {
    println!("[sc] kernel diagnostics");
    let openat2_line = match d.openat2_detail {
        OpenAt2Status::Ok => "OK        (RESOLVE_BENEATH path isolation active)".to_string(),
        OpenAt2Status::UnsupportedKernel => {
            "unavailable (kernel < 5.6; cap-std fallback in use)".to_string()
        }
        OpenAt2Status::DeniedByPolicy => {
            "ERROR     EPERM — likely Docker's default seccomp profile denying an \
             allow-listed syscall (security downgrade). Upgrade Docker (>= 20.10.10) or \
             pass --security-opt seccomp=<updated>.json. Continuing with cap-std fallback."
                .to_string()
        }
        OpenAt2Status::NotApplicable => "n/a (non-Linux dev platform)".to_string(),
    };
    println!("[sc]   openat2         {openat2_line}");
    println!(
        "[sc]   statx.btime     {}",
        if d.kernel_caps.statx_btime {
            "OK (inode-reuse detection active)"
        } else {
            "unavailable"
        }
    );
    println!(
        "[sc]   renameat2       {}",
        if d.kernel_caps.renameat2 {
            "OK"
        } else {
            "unavailable (renameat fallback)"
        }
    );
    println!(
        "[sc]   copy_file_range {}",
        if d.kernel_caps.copy_file_range {
            "OK"
        } else {
            "unavailable (buffer-copy fallback)"
        }
    );
    match d.kernel_caps.landlock {
        Some(abi) => println!("[sc]   landlock        ABI {abi} (path sandbox active)"),
        None => println!("[sc]   landlock        not probed yet (see hardening.rs / M6)"),
    }
    match d.inotify_max_watches {
        Some(n) => println!("[sc]   inotify watches {n} available"),
        None => println!("[sc]   inotify watches unknown (non-Linux platform)"),
    }
    match d.selinux {
        SelinuxStatus::Enforcing => println!("[sc]   selinux         enforcing"),
        SelinuxStatus::Permissive => println!("[sc]   selinux         permissive"),
        SelinuxStatus::Disabled => println!("[sc]   selinux         disabled/not present"),
        SelinuxStatus::NotApplicable => {
            println!("[sc]   selinux         n/a (non-Linux dev platform)")
        }
    }

    for s in &d.shares {
        if s.rejected {
            println!(
                "[sc]   share {:?} ({}): {:?} — REJECTED (inode instability; use a volume/bind mount, not the container writable layer)",
                s.name, s.host_path.display(), s.fstype
            );
        } else {
            println!(
                "[sc]   share {:?} ({}): {:?}",
                s.name,
                s.host_path.display(),
                s.fstype
            );
        }
        if let Some(access) = &s.access {
            if !access.readable {
                println!(
                    "[sc]   WARNING: share {:?} ({}) is not actually readable by this process \
                     (ACL, capability, or read-only mount overriding the mode bits) — reads \
                     through this share will fail at request time despite registering cleanly.",
                    s.name, s.host_path.display()
                );
            }
            if access.write_mismatch {
                println!(
                    "[sc]   WARNING: share {:?} ({}) looks writable by its permission bits, but \
                     the real access check disagrees (ACL, capability, or read-only mount) — \
                     writes through this share will fail at request time.",
                    s.name, s.host_path.display()
                );
            }
        }
    }

    if d.selinux == SelinuxStatus::Enforcing && !d.shares.is_empty() {
        println!(
            "[sc]   WARNING: SELinux is enforcing. A share bind-mounted with `:Z` \
             (uppercase) gets an exclusive label, cutting off any other container \
             (Jellyfin, Samba) reading the same host directory — reads/writes from \
             that other service will fail with EACCES. Use `:z` (lowercase, shared \
             label) or no label on any mount another service also touches \
."
        );
    }

    if d.master_key_inside_data_dir {
        println!(
            "[sc]   WARNING: master key file is inside the data directory — a backup of the \
             data volume will include the key, defeating encryption-at-rest. Move it to a \
             separate secret/volume."
        );
    }
    if d.master_key_generated {
        println!("[sc]   master key: generated (first run)");
    }

    println!(
        "[sc]   DB size: {} bytes, size_guard={}",
        d.db_bytes, d.db_size_guard
    );
    if let (Some(total), Some(free)) = (d.volume_total, d.volume_free) {
        println!(
            "[sc]   data volume: total {} bytes, free {} bytes (floor: db.min_free_bytes={} bytes)",
            total, free, d.min_free_bytes
        );
        if free < d.min_free_bytes {
            println!(
                "[sc]   -> BELOW THE FLOOR: metadata growth (fileid/dead-property allocation) \
                 is refused until free space recovers"
            );
        }
    }
    if !d.db_size_guard {
        if let Some((_, max_bytes)) = d.size_guard_recommendation {
            println!(
                "[sc]   -> data volume is small; recommend db.size_guard=true, db.max_bytes={max_bytes} bytes"
            );
        }
    }

    match &d.smb_bind_result {
        Ok(()) => println!("[sc]   smb bind: OK"),
        Err(e) => println!("[sc]   smb bind: REFUSED — {e}"),
    }

    let tp = &d.trusted_proxies;
    if tp.accepted.is_empty() {
        println!("[sc]   trusted proxies: NONE configured");
        println!("[sc]     Forwarding headers (CF-Connecting-IP, X-Forwarded-For) are");
        println!("[sc]     ignored, so every client is identified by the socket peer.");
        println!("[sc]     If this server sits behind Cloudflare or any reverse proxy");
        println!("[sc]     that is ONE address for everybody: the login brute-force");
        println!("[sc]     gate, the API rate limiter and the audit log all collapse");
        println!("[sc]     onto a single bucket, and one attacker locks out every user.");
        println!("[sc]     Set `trusted_proxies` / SC_TRUSTED_PROXIES to the proxy's");
        println!("[sc]     CIDRs. Correct as-is");
        println!("[sc]     only if clients reach this socket directly.");
    } else {
        println!("[sc]   trusted proxies: {} CIDR(s)", tp.accepted.len());
    }
    if !tp.rejected.is_empty() {
        println!(
            "[sc]   ERROR: {} trusted-proxy entr(ies) are not valid CIDRs and were DROPPED: {}",
            tp.rejected.len(),
            tp.rejected.join(", ")
        );
        println!(
            "[sc]     Those proxies are not trusted. Expected `addr/prefix`, e.g. 173.245.48.0/20."
        );
    }
    for w in &tp.wildcard {
        println!(
            "[sc]   WARNING: trusted-proxy entry {w:?} matches every address — any client can then \
             set its own CF-Connecting-IP and pick which rate-limit bucket to spend."
        );
    }

    if d.single_origin {
        println!("[sc]   content origin: NONE — serving user content from the app origin");
        println!("[sc]     A stored-XSS in an uploaded file can reach the session cookie.");
        println!("[sc]     Set `content_hosts` to a separate hostname to restore the");
        println!("[sc]     isolation. Acceptable for local use.");
    }

    // `cfg!(...)`, not `#[cfg(...)]`: the field above is always populated (it
    // is a pure function of `cfg`'s own fields, nothing compat-specific to
    // compile out), so gating the *print* on a runtime constant is simpler
    // than threading a second, conditionally-compiled copy of this block
    // through every caller. The dead branch costs nothing — this is a
    // compile-time-constant condition, so rustc drops whichever side does not
    // apply to this build.
    if cfg!(feature = "compat-nc") {
        use crate::config::CompatCanonicalUrl;
        match &d.compat_canonical_url {
            CompatCanonicalUrl::Configured(url) => {
                println!("[sc]   compat canonical_url: {url} (explicit `compat_canonical_url`)");
            }
            CompatCanonicalUrl::Derived(url) => {
                println!(
                    "[sc]   compat canonical_url: {url} (derived — the sole `app_hosts` entry)"
                );
                println!("[sc]     This is the URL Login Flow v2 hands a real device's system");
                println!("[sc]     browser, which then binds to it permanently. Set");
                println!("[sc]     `compat_canonical_url` explicitly before depending on this in");
                println!("[sc]     production.");
            }
            CompatCanonicalUrl::Ambiguous { app_host_count } => {
                println!(
                    "[sc]   compat canonical_url: UNRESOLVED — compatibility layer NOT mounted"
                );
                println!(
                    "[sc]     `compat_canonical_url` is unset and `app_hosts` has {app_host_count} \
                     entries: no single origin to hand a real device without guessing."
                );
                println!(
                    "[sc]     Set `compat_canonical_url` to enable compat client support."
                );
            }
            CompatCanonicalUrl::Invalid(value) => {
                println!(
                    "[sc]   compat canonical_url: INVALID ({value:?}) — compatibility layer NOT mounted"
                );
                println!("[sc]     `compat_canonical_url` must be an absolute http(s):// origin.");
            }
        }
    }
}

/// Enumerate this host's non-loopback interface addresses, for feeding
/// `sc_smb::SmbOrchestrator::validate_bind`. Linux-only real implementation
/// (`getifaddrs`); everywhere else (dev platforms) returns an empty list —
/// SMB is a Linux-deployment-only feature, so this
/// only needs to be real where SMB actually runs.
#[cfg(target_os = "linux")]
pub fn local_interface_addrs() -> Vec<IpAddr> {
    use std::mem::MaybeUninit;
    let mut out = Vec::new();
    unsafe {
        let mut ifap: MaybeUninit<*mut libc::ifaddrs> = MaybeUninit::uninit();
        if libc::getifaddrs(ifap.as_mut_ptr()) != 0 {
            return out;
        }
        let head = ifap.assume_init();
        let mut cur = head;
        while !cur.is_null() {
            let ifa = &*cur;
            if !ifa.ifa_addr.is_null() {
                let family = (*ifa.ifa_addr).sa_family as i32;
                if family == libc::AF_INET {
                    let sa = &*(ifa.ifa_addr as *const libc::sockaddr_in);
                    let ip = std::net::Ipv4Addr::from(u32::from_be(sa.sin_addr.s_addr));
                    out.push(IpAddr::V4(ip));
                } else if family == libc::AF_INET6 {
                    let sa = &*(ifa.ifa_addr as *const libc::sockaddr_in6);
                    let ip = std::net::Ipv6Addr::from(sa.sin6_addr.s6_addr);
                    out.push(IpAddr::V6(ip));
                }
            }
            cur = ifa.ifa_next;
        }
        libc::freeifaddrs(head);
    }
    out
}

#[cfg(not(target_os = "linux"))]
pub fn local_interface_addrs() -> Vec<IpAddr> {
    Vec::new()
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn guard_recommendation_thresholds() {
        const GIB: u64 = 1024 * 1024 * 1024;
        assert_eq!(guard_recommendation(Some(29 * GIB)), Some((true, 2 * GIB)));
        assert_eq!(guard_recommendation(Some(128 * GIB)), Some((true, 8 * GIB)));
        assert_eq!(guard_recommendation(Some(512 * GIB)), None);
        assert_eq!(guard_recommendation(None), None);
    }

    /// A typo in `SC_TRUSTED_PROXIES` silently un-trusts a proxy (`App::build`
    /// drops unparseable entries with `filter_map`), so startup has to be the
    /// place that notices.
    #[test]
    fn trusted_proxy_entries_are_classified_not_silently_dropped() {
        let tp = classify_trusted_proxies(&[
            "173.245.48.0/20".into(),
            "2400:cb00::/32".into(),
            "10.0.0.1".into(), // no prefix — rejected by Cidr::parse
            "0.0.0.0/0".into(),
        ]);
        assert_eq!(
            tp.accepted,
            vec!["173.245.48.0/20", "2400:cb00::/32", "0.0.0.0/0"]
        );
        assert_eq!(tp.rejected, vec!["10.0.0.1"]);
        assert_eq!(tp.wildcard, vec!["0.0.0.0/0"]);
    }

    #[test]
    fn no_trusted_proxies_is_the_reportable_default() {
        assert!(classify_trusted_proxies(&[]).accepted.is_empty());
    }

    /// The free-space figure a test passes when the volume is not what it is
    /// testing — also the real "unknown" sentinel `FREE_BYTES` starts at.
    const PLENTY: u64 = u64::MAX;

    /// Baseline: nothing rejected, SMB bind fine (or unused), DB well under
    /// its cap. A fresh `fn base()` per test rather than a shared `static` —
    /// `smb_bind_result` isn't `Clone`.
    fn base() -> Diagnostics {
        Diagnostics {
            kernel_caps: sc_vfs::KernelCaps {
                openat2: false,
                statx_btime: false,
                renameat2: false,
                copy_file_range: false,
                landlock: None,
            },
            openat2_detail: OpenAt2Status::NotApplicable,
            inotify_max_watches: None,
            selinux: SelinuxStatus::NotApplicable,
            shares: Vec::new(),
            master_key_inside_data_dir: false,
            master_key_generated: false,
            db_bytes: 0,
            db_size_guard: false,
            max_bytes: 4 * 1024 * 1024 * 1024,
            volume_total: None,
            volume_free: None,
            min_free_bytes: 1024 * 1024 * 1024,
            size_guard_recommendation: None,
            smb_bind_result: Ok(()),
            single_origin: false,
            trusted_proxies: TrustedProxies::default(),
            compat_canonical_url: crate::config::CompatCanonicalUrl::Configured(
                "https://cloud.example.com".into(),
            ),
        }
    }

    /// `GET /api/health` used to be a hardcoded `{"status":"ok"}` no matter
    /// what. These prove the signal it needs — `is_degraded` — actually
    /// reacts to every non-fatal condition this struct tracks, so the
    /// eventual wiring (`routes.rs`, out of this change's scope) has
    /// something real to consult instead of a constant.
    #[test]
    fn a_healthy_deployment_reports_nothing_degraded() {
        let d = base();
        assert!(!d.is_degraded(0, PLENTY));
        assert!(d.degraded_reasons(0, PLENTY).is_empty());
    }

    #[test]
    fn a_rejected_share_is_degraded_but_the_public_signal_stays_a_bare_bool() {
        let mut d = base();
        d.shares.push(FsProbe {
            name: "docs".into(),
            host_path: PathBuf::from("/data/docs"),
            fstype: sc_vfs::FsType::Other(0),
            rejected: true,
            access: None,
        });
        assert!(d.is_degraded(0, PLENTY));
        assert_eq!(d.degraded_reasons(0, PLENTY), vec!["share_rejected"]);
    }

    #[test]
    fn a_refused_smb_bind_is_degraded() {
        let mut d = base();
        d.smb_bind_result = Err("address already in use".into());
        assert!(d.is_degraded(0, PLENTY));
        assert_eq!(d.degraded_reasons(0, PLENTY), vec!["smb_bind_failed"]);
    }

    #[test]
    fn the_db_size_guard_only_degrades_when_enabled_and_over_the_line() {
        let mut d = base();
        d.db_size_guard = true;
        d.max_bytes = 1000;
        // Under the cap: fine even with the guard on.
        assert!(!d.is_degraded(999, PLENTY));
        // At/over the cap: degraded.
        assert!(d.is_degraded(1000, PLENTY));
        assert_eq!(d.degraded_reasons(1000, PLENTY), vec!["db_size_guard_tripped"]);

        // Same byte count, guard disabled: the threshold alone means nothing
        // — `db.size_guard` not being enabled must not manufacture a
        // degraded status the operator never opted into.
        d.db_size_guard = false;
        assert!(!d.is_degraded(5000, PLENTY));
    }

    /// "**Independent of the guard** ... it
    /// cannot be turned off." Both halves of that sentence are the test.
    #[test]
    fn the_free_space_floor_is_independent_of_the_size_guard_and_has_no_off_switch() {
        let mut d = base();
        d.min_free_bytes = 1000;
        assert!(!d.is_degraded(0, 1000), "at the floor is not below it");
        assert_eq!(d.degraded_reasons(0, 999), vec!["db_free_space_low"]);

        // `db.size_guard = false` is the operator turning the *policy* ceiling
        // off. It must not take the floor with it.
        d.db_size_guard = false;
        assert!(d.is_degraded(0, 0));

        // An unsampled/unsupported platform reads as `u64::MAX`, which must
        // never be mistaken for a full disk.
        assert!(!d.is_degraded(0, PLENTY));
    }

    #[test]
    fn multiple_simultaneous_problems_all_surface() {
        let mut d = base();
        d.shares.push(FsProbe {
            name: "x".into(),
            host_path: PathBuf::from("/x"),
            fstype: sc_vfs::FsType::Other(0),
            rejected: true,
            access: None,
        });
        d.smb_bind_result = Err("nope".into());
        d.db_size_guard = true;
        d.max_bytes = 10;
        d.min_free_bytes = 500;
        assert_eq!(
            d.degraded_reasons(10, 499),
            vec![
                "share_rejected",
                "smb_bind_failed",
                "db_size_guard_tripped",
                "db_free_space_low"
            ]
        );
    }

    /// The floor is only a floor if the sampler actually closes the gate.
    /// Driven with synthetic thresholds rather than a real full disk: the
    /// point under test is the wiring, not `statvfs`.
    #[test]
    fn the_sampler_opens_and_closes_the_metadata_write_gate() {
        let dir = tempfile::tempdir().unwrap();
        let meta = sc_meta::MetaStore::open_in_memory().unwrap();
        assert!(!meta.writes_blocked());

        // No real volume has `u64::MAX` free, so this always trips.
        sample_storage_once(dir.path(), u64::MAX, &meta);
        assert!(meta.writes_blocked());
        // ...and no volume has less than zero, so this always clears.
        sample_storage_once(dir.path(), 0, &meta);
        assert!(!meta.writes_blocked());
    }

    /// The size guard compares against `DB_BYTES`, so a database that grows
    /// past the cap while the server runs only trips it if something keeps
    /// republishing the size. Startup and `gc` did; nothing else did.
    #[test]
    fn the_sampler_republishes_the_database_size() {
        use std::sync::atomic::Ordering;
        let dir = tempfile::tempdir().unwrap();
        let meta = sc_meta::MetaStore::open_in_memory().unwrap();

        crate::bridge::DB_BYTES.store(0, Ordering::Relaxed);
        sample_storage_once(dir.path(), 0, &meta);
        let sampled = crate::bridge::DB_BYTES.load(Ordering::Relaxed);
        assert_eq!(sampled, meta.size_bytes().unwrap());
        assert!(sampled > 0, "an open store is never zero pages");
    }

    #[test]
    fn openat2_detail_reports_something_on_this_platform() {
        // Just prove the probe runs without panicking; the exact status is
        // platform-dependent (this crate's mandated dev platform is Windows,
        // where it's always `NotApplicable`).
        let _ = probe_openat2_detail();
    }

    #[test]
    fn selinux_probe_runs_without_panicking() {
        // Platform-dependent like `probe_openat2_detail` above; this crate's
        // mandated dev platform is Windows, where it's always `NotApplicable`.
        // Real enforcing/permissive/disabled coverage only exists on Linux
        // (verified manually against the Rocky VM, which has selinuxfs).
        let _ = probe_selinux();
    }
}

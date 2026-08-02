//! One-shot runtime feature probing. Run once at startup; `openat2` failing
//! with `ENOSYS` means an old kernel, `EPERM` means seccomp (Docker's
//! default profile) blocked it — the two deserve different log messages
//! upstream (that decision belongs to the caller; we just report the facts).

#[derive(Clone, Copy, Debug, Default, PartialEq, Eq)]
pub struct KernelCaps {
    pub openat2: bool,
    pub statx_btime: bool,
    pub renameat2: bool,
    pub copy_file_range: bool,
    /// Landlock ABI version, if available. Full probing is wired up
    /// alongside the M6 process-isolation work; until then this is always
    /// `None` rather than guessing.
    pub landlock: Option<u32>,
}

#[cfg(target_os = "linux")]
pub fn detect_kernel_caps() -> KernelCaps {
    linux_probe::detect()
}

#[cfg(not(target_os = "linux"))]
pub fn detect_kernel_caps() -> KernelCaps {
    // Portable/dev fallback: none of these syscalls exist outside Linux.
    KernelCaps {
        openat2: false,
        statx_btime: false,
        renameat2: false,
        copy_file_range: false,
        landlock: None,
    }
}

#[cfg(target_os = "linux")]
mod linux_probe {
    use super::KernelCaps;
    use rustix::fs::{Mode, OFlags, ResolveFlags, CWD};
    use rustix::io::Errno;

    pub(super) fn detect() -> KernelCaps {
        KernelCaps {
            openat2: probe_openat2(),
            statx_btime: probe_statx_btime(),
            renameat2: probe_renameat2(),
            copy_file_range: probe_copy_file_range(),
            landlock: None,
        }
    }

    fn probe_openat2() -> bool {
        match rustix::fs::openat2(
            CWD,
            ".",
            OFlags::RDONLY | OFlags::DIRECTORY | OFlags::CLOEXEC,
            Mode::empty(),
            ResolveFlags::empty(),
        ) {
            Ok(_) => true,
            // ENOSYS: kernel too old. Anything else (including EPERM under a
            // seccomp filter that denies rather than ENOSYS) is treated as
            // "the syscall exists" so we don't downgrade over a policy
            // decision rather than absence.
            Err(Errno::NOSYS) => false,
            Err(_) => true,
        }
    }

    fn probe_statx_btime() -> bool {
        match rustix::fs::statx(
            CWD,
            ".",
            rustix::fs::AtFlags::empty(),
            rustix::fs::StatxFlags::BTIME,
        ) {
            Ok(s) => (s.stx_mask & rustix::fs::StatxFlags::BTIME.bits()) != 0,
            Err(_) => false,
        }
    }

    fn probe_renameat2() -> bool {
        // Probe against a path that (almost certainly) doesn't exist, so the
        // expected outcome is ENOENT (syscall exists, nothing to rename) vs
        // ENOSYS (syscall absent). No real file is touched.
        !matches!(
            rustix::fs::renameat_with(
                CWD,
                ".sc-vfs-cap-probe-absent",
                CWD,
                ".sc-vfs-cap-probe-absent-2",
                rustix::fs::RenameFlags::NOREPLACE,
            ),
            Err(Errno::NOSYS)
        )
    }

    fn probe_copy_file_range() -> bool {
        // No side-effect-free probe exists (it needs two real fds). Assume
        // available — universal on any kernel >= 4.5 we still support — and
        // let call sites fall back to a read/write loop on ENOSYS.
        true
    }
}

//! Process-level self-restriction: Landlock path sandbox + a minimal seccomp
//! filter (`ARCHITECTURE.md` §2.4, `DEPLOYMENT.md` §10 checklist item 8).
//!
//! Both are Linux-only, best-effort, and applied *after* shares/the data
//! directory are opened (so the paths being restricted to are already valid
//! file descriptors/paths) — never a hard failure, since older kernels or a
//! restrictive seccomp profile (the Docker-default-profile irony: it can
//! itself block `landlock_*`, `DEPLOYMENT.md` §2) legitimately can't do
//! this, and the rest of the security model (path validation in `sc-vfs`,
//! running as an unprivileged uid) still holds without it.

#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum HardeningStatus {
    FullyEnforced,
    PartiallyEnforced,
    Unavailable,
}

pub struct HardeningResult {
    pub landlock: HardeningStatus,
    pub seccomp: HardeningStatus,
}

#[cfg(target_os = "linux")]
pub fn apply(restrict_paths: &[std::path::PathBuf]) -> HardeningResult {
    HardeningResult {
        landlock: apply_landlock(restrict_paths),
        seccomp: apply_seccomp(),
    }
}

#[cfg(not(target_os = "linux"))]
pub fn apply(_restrict_paths: &[std::path::PathBuf]) -> HardeningResult {
    HardeningResult {
        landlock: HardeningStatus::Unavailable,
        seccomp: HardeningStatus::Unavailable,
    }
}

#[cfg(target_os = "linux")]
fn apply_landlock(restrict_paths: &[std::path::PathBuf]) -> HardeningStatus {
    use landlock::{
        Access, AccessFs, PathBeneath, PathFd, Ruleset, RulesetAttr, RulesetCreatedAttr,
        RulesetStatus, ABI,
    };

    let abi = ABI::V2;
    let access_all = AccessFs::from_all(abi);

    let ruleset = match Ruleset::default().handle_access(access_all) {
        Ok(r) => r,
        Err(e) => {
            tracing::warn!(error = %e, "Landlock unavailable (handle_access failed); continuing without it");
            return HardeningStatus::Unavailable;
        }
    };

    let mut created = match ruleset.create() {
        Ok(c) => c,
        Err(e) => {
            tracing::warn!(error = %e, "Landlock unavailable (ruleset create failed); continuing without it");
            return HardeningStatus::Unavailable;
        }
    };

    for p in restrict_paths {
        let fd = match PathFd::new(p) {
            Ok(fd) => fd,
            Err(e) => {
                tracing::warn!(path = %p.display(), error = %e, "Landlock: skipping path (couldn't open)");
                continue;
            }
        };
        // `add_rule` consumes `created` and doesn't hand it back on error,
        // so a failure here can't be "skip this path and keep going" —
        // it's "give up on Landlock for this process" instead.
        created = match created.add_rule(PathBeneath::new(fd, access_all)) {
            Ok(c) => c,
            Err(e) => {
                tracing::warn!(path = %p.display(), error = %e, "Landlock: failed to add rule for path; abandoning Landlock setup");
                return HardeningStatus::Unavailable;
            }
        };
    }

    match created.restrict_self() {
        Ok(status) => match status.ruleset {
            RulesetStatus::FullyEnforced => HardeningStatus::FullyEnforced,
            RulesetStatus::PartiallyEnforced => HardeningStatus::PartiallyEnforced,
            RulesetStatus::NotEnforced => {
                tracing::warn!(
                    "Landlock not enforced by the running kernel; continuing without it"
                );
                HardeningStatus::Unavailable
            }
        },
        Err(e) => {
            tracing::warn!(error = %e, "Landlock restrict_self failed; continuing without it");
            HardeningStatus::Unavailable
        }
    }
}

/// A small deny-list seccomp filter: `ptrace`, `process_vm_readv`/`_writev`,
/// `mount`, `kexec_load`, `bpf`, `userfaultfd` — see `ARCHITECTURE.md` §2.4.
/// Everything else is allowed; this is not a sandbox on its own, it's a
/// second line of defense alongside Landlock and the unprivileged uid.
///
/// x86_64 syscall numbers (stable Linux ABI, hardcoded rather than sourced
/// from a crate since no seccomp-filter-builder crate is in the workspace
/// dependency set):
#[cfg(target_os = "linux")]
fn apply_seccomp() -> HardeningStatus {
    #[cfg(target_arch = "x86_64")]
    const DENIED_SYSCALLS: &[u32] = &[
        101, // ptrace
        165, // mount
        246, // kexec_load
        310, // process_vm_readv
        311, // process_vm_writev
        320, // kexec_file_load
        321, // bpf
        323, // userfaultfd
    ];
    #[cfg(not(target_arch = "x86_64"))]
    const DENIED_SYSCALLS: &[u32] = &[];

    // Const-false on x86_64 and const-true everywhere else — that is the arch
    // guard, not a redundant check. clippy sees only the target it is
    // compiling for and calls one half of it dead.
    #[allow(clippy::const_is_empty)]
    if DENIED_SYSCALLS.is_empty() {
        tracing::warn!("seccomp: no syscall table for this architecture; skipping");
        return HardeningStatus::Unavailable;
    }

    // BPF opcodes (linux/filter.h / linux/bpf_common.h). Each is spelled out
    // as the OR of its named parts so it can be checked against the header by
    // eye. Several of those parts are genuinely `0x00` there — `BPF_LD`,
    // `BPF_W`, `BPF_K` — so clippy sees `0 | 0` and `x | 0` and objects. It is
    // right about the arithmetic and wrong about the code: collapsing these to
    // their computed values would make a filter that decides what the process
    // may do unreviewable against the spec it comes from.
    #[allow(clippy::eq_op, clippy::identity_op)]
    const BPF_LD_W_ABS: u16 = 0x00 /*BPF_LD*/ | 0x00 /*BPF_W*/ | 0x20 /*BPF_ABS*/;
    #[allow(clippy::identity_op)]
    const BPF_JMP_JEQ_K: u16 = 0x05 /*BPF_JMP*/ | 0x10 /*BPF_JEQ*/ | 0x00 /*BPF_K*/;
    #[allow(clippy::identity_op)]
    const BPF_RET_K: u16 = 0x06 /*BPF_RET*/ | 0x00 /*BPF_K*/;
    const SECCOMP_RET_ALLOW: u32 = 0x7fff_0000;
    const SECCOMP_RET_ERRNO: u32 = 0x0005_0000;
    const EPERM: u32 = 1;

    // offsetof(struct seccomp_data, nr) == 0.
    let mut prog: Vec<libc::sock_filter> = Vec::with_capacity(DENIED_SYSCALLS.len() + 3);
    prog.push(libc::sock_filter {
        code: BPF_LD_W_ABS,
        jt: 0,
        jf: 0,
        k: 0,
    });

    let n = DENIED_SYSCALLS.len() as u8;
    for (i, syscall_nr) in DENIED_SYSCALLS.iter().enumerate() {
        // Jump forward to the ERRNO instruction (skipping remaining
        // compares + the ALLOW instruction) on match; fall through to the
        // next compare otherwise.
        let jt = n - i as u8; // remaining compares after this one, + 1 for ALLOW
        prog.push(libc::sock_filter {
            code: BPF_JMP_JEQ_K,
            jt,
            jf: 0,
            k: *syscall_nr,
        });
    }
    prog.push(libc::sock_filter {
        code: BPF_RET_K,
        jt: 0,
        jf: 0,
        k: SECCOMP_RET_ALLOW,
    });
    prog.push(libc::sock_filter {
        code: BPF_RET_K,
        jt: 0,
        jf: 0,
        k: SECCOMP_RET_ERRNO | EPERM,
    });

    let fprog = libc::sock_fprog {
        len: prog.len() as libc::c_ushort,
        filter: prog.as_mut_ptr(),
    };

    // Required before installing a filter as an unprivileged process.
    const PR_SET_NO_NEW_PRIVS: libc::c_int = 38;
    const PR_SET_SECCOMP: libc::c_int = 22;
    const SECCOMP_MODE_FILTER: libc::c_ulong = 2;

    unsafe {
        if libc::prctl(PR_SET_NO_NEW_PRIVS, 1, 0, 0, 0) != 0 {
            tracing::warn!("seccomp: PR_SET_NO_NEW_PRIVS failed; skipping filter install");
            return HardeningStatus::Unavailable;
        }
        let ret = libc::prctl(
            PR_SET_SECCOMP,
            SECCOMP_MODE_FILTER,
            &fprog as *const libc::sock_fprog as libc::c_ulong,
            0,
            0,
        );
        if ret != 0 {
            tracing::warn!("seccomp: PR_SET_SECCOMP failed (kernel too old, or already filtered); continuing without it");
            return HardeningStatus::Unavailable;
        }
    }
    HardeningStatus::FullyEnforced
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn apply_never_panics() {
        // On the mandated Windows dev platform this is always Unavailable;
        // the point of the test is just "doesn't panic, has a sane shape".
        let r = apply(&[std::path::PathBuf::from(".")]);
        #[cfg(not(target_os = "linux"))]
        {
            assert_eq!(r.landlock, HardeningStatus::Unavailable);
            assert_eq!(r.seccomp, HardeningStatus::Unavailable);
        }
        let _ = r;
    }
}

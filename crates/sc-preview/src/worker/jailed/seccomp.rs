//! The seccomp-BPF syscall allow-list for the preview worker
//! (`DESIGN-PREVIEW.md` §4.2, step ③).
//!
//! # Why the filter is hand-built instead of using a crate
//!
//! Three reasons, in order of weight:
//!
//! 1. **There is already a hand-built seccomp filter in this workspace.**
//!    `sc-server::hardening` installs a small deny-list for the server
//!    process itself (`ARCHITECTURE.md` §2.4) with the same `libc::sock_filter`
//!    primitives used here. Pulling in `seccompiler` for the second filter
//!    would leave the tree with two different mechanisms for the same job.
//! 2. **The policy is flat.** Twenty-two syscall numbers, no argument
//!    inspection, no ranges, no conditional actions. That is precisely the
//!    shape for which classic BPF is two dozen instructions of straight-line
//!    code — and it is the one place in the tree where "the entire policy the
//!    kernel will enforce is readable in one screen, with nothing in between"
//!    beats the ergonomics of a builder API. `seccompiler`'s value is
//!    argument filtering and a JSON policy format, neither of which this
//!    filter uses.
//! 3. **No new supply chain.** `libc` is already a workspace dependency and
//!    already in `Cargo.lock`, so the jail around the project's most
//!    dangerous code path adds no new crate at all.
//!
//! Where this module improves on the `sc-server` precedent: syscall numbers
//! come from `libc::SYS_*` rather than being hardcoded, the architecture is
//! checked before the syscall number is trusted, the x32 ABI is rejected,
//! failures are fatal instead of a warning, and the assembled program's jump
//! offsets are unit-tested.
//!
//! # Fail-closed
//!
//! [`install`] returns `Err` rather than silently doing nothing if it cannot
//! install the policy it promised — including on an architecture this module
//! has no `AUDIT_ARCH` for. The caller ([`super::enter_jail`]) treats that as
//! fatal for the worker child: a worker that could not be jailed must not run
//! a decoder.

#[cfg(any(target_arch = "x86_64", target_arch = "aarch64"))]
pub use filter::install;

#[cfg(not(any(target_arch = "x86_64", target_arch = "aarch64")))]
use crate::error::PreviewError;

/// Fail-closed stub for architectures this module has no verified
/// `AUDIT_ARCH` / syscall-number mapping for. Refusing to start is the
/// correct behaviour: the alternative is a worker running untrusted
/// decoders with no syscall filter while the docs claim otherwise.
#[cfg(not(any(target_arch = "x86_64", target_arch = "aarch64")))]
pub fn install() -> Result<(), PreviewError> {
    Err(PreviewError::Worker(
        "no seccomp filter is defined for this architecture; refusing to run an unjailed \
         preview worker"
            .into(),
    ))
}

#[cfg(any(target_arch = "x86_64", target_arch = "aarch64"))]
mod filter {
    use std::io;

    use crate::error::PreviewError;

    // --- classic BPF opcodes (linux/bpf_common.h) ---
    const BPF_LD: u16 = 0x00;
    const BPF_W: u16 = 0x00;
    const BPF_ABS: u16 = 0x20;
    const BPF_JMP: u16 = 0x05;
    const BPF_JEQ: u16 = 0x10;
    #[cfg(target_arch = "x86_64")]
    const BPF_JGE: u16 = 0x30;
    const BPF_K: u16 = 0x00;
    const BPF_RET: u16 = 0x06;

    // --- seccomp (linux/seccomp.h) ---
    const SECCOMP_SET_MODE_FILTER: libc::c_long = 1;
    /// `SECCOMP_MODE_FILTER`, spelled out rather than taken from `libc` so the
    /// pre-3.17 `prctl` fallback below does not depend on which `libc`
    /// version/target happens to export the constant.
    const SECCOMP_MODE_FILTER: libc::c_int = 2;
    const SECCOMP_RET_KILL_PROCESS: u32 = 0x8000_0000;
    const SECCOMP_RET_ALLOW: u32 = 0x7fff_0000;

    // --- `struct seccomp_data` field offsets ---
    const OFF_NR: u32 = 0;
    const OFF_ARCH: u32 = 4;

    /// The arch check is not decoration: without it, a syscall number that
    /// means something harmless under one ABI can mean something else
    /// entirely under another, and the filter would wave it through.
    #[cfg(target_arch = "x86_64")]
    const AUDIT_ARCH: u32 = 0xC000_003E; // AUDIT_ARCH_X86_64
    #[cfg(target_arch = "aarch64")]
    const AUDIT_ARCH: u32 = 0xC000_00B7; // AUDIT_ARCH_AARCH64

    /// On x86-64 the x32 ABI reports the same `arch` value with this bit set
    /// on the syscall number, so an x32 task would otherwise be matched
    /// against x86-64 syscall numbers. Reject the whole range.
    #[cfg(target_arch = "x86_64")]
    const X32_SYSCALL_BIT: u32 = 0x4000_0000;

    #[cfg(target_arch = "x86_64")]
    const PROLOGUE: usize = 4;
    #[cfg(not(target_arch = "x86_64"))]
    const PROLOGUE: usize = 3;

    const fn stmt(code: u16, k: u32) -> libc::sock_filter {
        libc::sock_filter { code, jt: 0, jf: 0, k }
    }

    const fn jump(code: u16, k: u32, jt: u8, jf: u8) -> libc::sock_filter {
        libc::sock_filter { code, jt, jf, k }
    }

    /// The allow-list from `DESIGN-PREVIEW.md` §4.2, plus `recvmsg`/`sendmsg`
    /// (the design doc's pseudo-code omits them, but they are how the worker
    /// receives its job and its fds in the first place — without them the
    /// worker cannot take a single job).
    ///
    /// Deliberately absent, and worth naming because each is a proof
    /// obligation the jail discharges: `openat` (no files by name, ever),
    /// `socket`/`connect`/`sendto` (no network), `clone`/`fork`/`execve` (no
    /// new processes), `ptrace`, `mprotect` (no W^X flips), `statx`.
    ///
    /// `statx` in particular is why the worker reads its input with a manual
    /// `read(2)` loop instead of `Read::read_to_end`: std's `File`
    /// specialization of `read_to_end` sizes the buffer via `File::metadata`,
    /// which on a modern kernel issues `statx`, which this filter kills.
    /// Keeping the list identical to the design doc was worth the four extra
    /// lines in the worker loop.
    fn allowed_syscalls() -> Vec<libc::c_long> {
        vec![
            libc::SYS_read,
            libc::SYS_pread64,
            libc::SYS_write,
            libc::SYS_pwrite64,
            libc::SYS_mmap,
            libc::SYS_munmap,
            libc::SYS_mremap,
            libc::SYS_brk,
            libc::SYS_close,
            libc::SYS_fstat,
            libc::SYS_lseek,
            libc::SYS_futex,
            libc::SYS_sched_yield,
            libc::SYS_exit,
            libc::SYS_exit_group,
            libc::SYS_rt_sigreturn,
            libc::SYS_rt_sigprocmask,
            libc::SYS_madvise,
            libc::SYS_getrandom,
            libc::SYS_clock_gettime,
            libc::SYS_recvmsg,
            libc::SYS_sendmsg,
        ]
    }

    /// Assemble the filter program.
    ///
    /// ```text
    ///   0                    A = seccomp_data.arch
    ///   1                    if A != AUDIT_ARCH        -> KILL
    ///   2                    A = seccomp_data.nr
    ///   3                    if A >= X32_SYSCALL_BIT   -> KILL   (x86_64 only)
    ///   PROLOGUE..+n-1       if A == allowed[i]        -> ALLOW
    ///   PROLOGUE+n           KILL                                (default)
    ///   PROLOGUE+n+1         ALLOW
    /// ```
    ///
    /// BPF jump offsets are unsigned 8-bit and relative to the *following*
    /// instruction, so the longest jump here must stay under 256. With 22
    /// entries there is a lot of headroom, but this checks rather than
    /// trusting that nobody ever grows the list.
    fn build_program(allowed: &[libc::c_long]) -> Result<Vec<libc::sock_filter>, PreviewError> {
        let n = allowed.len();
        let kill_idx = PROLOGUE + n;
        let allow_idx = kill_idx + 1;

        if kill_idx.saturating_sub(2) > u8::MAX as usize {
            return Err(PreviewError::Worker(format!(
                "seccomp allow-list of {n} syscalls does not fit in 8-bit BPF jump offsets"
            )));
        }

        let mut prog = Vec::with_capacity(allow_idx + 1);

        prog.push(stmt(BPF_LD | BPF_W | BPF_ABS, OFF_ARCH));
        prog.push(jump(
            BPF_JMP | BPF_JEQ | BPF_K,
            AUDIT_ARCH,
            0,
            (kill_idx - 2) as u8,
        ));
        prog.push(stmt(BPF_LD | BPF_W | BPF_ABS, OFF_NR));
        #[cfg(target_arch = "x86_64")]
        prog.push(jump(
            BPF_JMP | BPF_JGE | BPF_K,
            X32_SYSCALL_BIT,
            (kill_idx - 4) as u8,
            0,
        ));

        for (i, nr) in allowed.iter().enumerate() {
            let here = PROLOGUE + i;
            prog.push(jump(
                BPF_JMP | BPF_JEQ | BPF_K,
                *nr as u32,
                (allow_idx - here - 1) as u8,
                0,
            ));
        }

        prog.push(stmt(BPF_RET | BPF_K, SECCOMP_RET_KILL_PROCESS));
        prog.push(stmt(BPF_RET | BPF_K, SECCOMP_RET_ALLOW));

        debug_assert_eq!(prog.len(), allow_idx + 1);
        Ok(prog)
    }

    /// Set `PR_SET_NO_NEW_PRIVS`. Required before an unprivileged process may
    /// install a seccomp filter, and required by Landlock too; setting it
    /// here as well as letting the `landlock` crate do it keeps the two
    /// layers independent of each other's ordering.
    fn set_no_new_privs() -> Result<(), PreviewError> {
        // SAFETY: prctl with a fixed, argument-free option.
        let rc = unsafe { libc::prctl(libc::PR_SET_NO_NEW_PRIVS, 1, 0, 0, 0) };
        if rc != 0 {
            return Err(PreviewError::Worker(format!(
                "prctl(PR_SET_NO_NEW_PRIVS) failed: {}",
                io::Error::last_os_error()
            )));
        }
        Ok(())
    }

    /// Install the allow-list. Irreversible for the life of the process.
    ///
    /// Must be the **last** thing the jail does: everything after this call
    /// is restricted to the twenty-two syscalls above.
    pub fn install() -> Result<(), PreviewError> {
        set_no_new_privs()?;

        let mut prog = build_program(&allowed_syscalls())?;
        let fprog = libc::sock_fprog {
            len: prog.len() as libc::c_ushort,
            filter: prog.as_mut_ptr(),
        };

        // SAFETY: `fprog` points at a live, correctly-sized program for the
        // duration of the call; the kernel reads it and does not retain the
        // pointer.
        let rc = unsafe {
            libc::syscall(
                libc::SYS_seccomp,
                SECCOMP_SET_MODE_FILTER,
                0 as libc::c_long,
                &fprog as *const libc::sock_fprog,
            )
        };
        if rc == 0 {
            return Ok(());
        }

        let err = io::Error::last_os_error();
        if err.raw_os_error() == Some(libc::ENOSYS) {
            // Pre-3.17 kernels only have the prctl entry point. Same filter,
            // same semantics for a single-threaded process.
            // SAFETY: as above.
            let rc = unsafe {
                libc::prctl(
                    libc::PR_SET_SECCOMP,
                    SECCOMP_MODE_FILTER,
                    &fprog as *const libc::sock_fprog,
                    0,
                    0,
                )
            };
            if rc == 0 {
                return Ok(());
            }
            return Err(PreviewError::Worker(format!(
                "prctl(PR_SET_SECCOMP, SECCOMP_MODE_FILTER) failed: {}",
                io::Error::last_os_error()
            )));
        }

        Err(PreviewError::Worker(format!(
            "seccomp(SECCOMP_SET_MODE_FILTER) failed: {err}"
        )))
    }

    #[cfg(test)]
    mod tests {
        use super::*;

        /// The program has to be well-formed *before* it reaches the kernel:
        /// a bad jump offset is a filter that silently allows something.
        #[test]
        fn program_is_well_formed_and_every_jump_lands_in_range() {
            let prog = build_program(&allowed_syscalls()).unwrap();
            let n = prog.len();
            assert!(n > allowed_syscalls().len());

            assert_eq!(prog[n - 2].code, BPF_RET | BPF_K);
            assert_eq!(prog[n - 2].k, SECCOMP_RET_KILL_PROCESS);
            assert_eq!(prog[n - 1].code, BPF_RET | BPF_K);
            assert_eq!(prog[n - 1].k, SECCOMP_RET_ALLOW);

            for (i, ins) in prog.iter().enumerate() {
                if ins.code & BPF_JMP != BPF_JMP {
                    continue;
                }
                for off in [ins.jt, ins.jf] {
                    let target = i + 1 + off as usize;
                    assert!(target < n, "instruction {i} jumps out of the program");
                }
            }
        }

        #[test]
        fn arch_is_checked_before_the_syscall_number_is_ever_loaded() {
            let prog = build_program(&allowed_syscalls()).unwrap();
            assert_eq!(prog[0].code, BPF_LD | BPF_W | BPF_ABS);
            assert_eq!(prog[0].k, OFF_ARCH);
            assert_eq!(prog[1].k, AUDIT_ARCH);
            // A mismatched arch must land exactly on KILL, not on ALLOW.
            let target = 1 + 1 + prog[1].jf as usize;
            assert_eq!(prog[target].k, SECCOMP_RET_KILL_PROCESS);
        }

        #[cfg(target_arch = "x86_64")]
        #[test]
        fn x32_syscalls_are_killed_rather_than_matched_against_x86_64_numbers() {
            let prog = build_program(&allowed_syscalls()).unwrap();
            assert_eq!(prog[3].code, BPF_JMP | BPF_JGE | BPF_K);
            assert_eq!(prog[3].k, X32_SYSCALL_BIT);
            let target = 3 + 1 + prog[3].jt as usize;
            assert_eq!(prog[target].k, SECCOMP_RET_KILL_PROCESS);
        }

        #[test]
        fn each_allowed_syscall_jumps_to_allow_and_a_miss_falls_through() {
            let allowed = allowed_syscalls();
            let prog = build_program(&allowed).unwrap();
            let allow_idx = prog.len() - 1;

            for (i, nr) in allowed.iter().enumerate() {
                let ins = prog[PROLOGUE + i];
                assert_eq!(ins.k, *nr as u32);
                assert_eq!(PROLOGUE + i + 1 + ins.jt as usize, allow_idx);
                assert_eq!(ins.jf, 0, "a non-match must fall through, never jump");
            }
        }

        /// The syscalls the jail exists to deny. If one of these ever appears
        /// in the allow-list, the proofs in `examples/jail_proof.rs` stop
        /// meaning what they say they mean.
        #[test]
        fn the_denied_syscalls_are_actually_absent() {
            let allowed = allowed_syscalls();
            for (name, nr) in [
                ("openat", libc::SYS_openat),
                ("socket", libc::SYS_socket),
                ("connect", libc::SYS_connect),
                ("clone", libc::SYS_clone),
                ("execve", libc::SYS_execve),
                ("ptrace", libc::SYS_ptrace),
                ("mprotect", libc::SYS_mprotect),
                ("statx", libc::SYS_statx),
            ] {
                assert!(!allowed.contains(&nr), "{name} must not be allowed");
            }
        }
    }
}

//go:build linux

package unixprobe

import "golang.org/x/sys/unix"

// Syscalls is the set the path layer, the upload path and the worker socket
// resolve through. Referencing the wrappers as values is enough: a rename, a
// removal or a changed signature fails the build.
func Syscalls() []any {
	var (
		how unix.OpenHow
		stx unix.Statx_t
		sfs unix.Statfs_t
		rl  unix.Rlimit
	)
	return []any{
		unix.Openat2, how,
		unix.Statx, stx,
		unix.Renameat2,
		unix.CopyFileRange,
		unix.CloseRange,
		unix.Fstatfs, sfs,
		unix.Getdents,
		unix.ParseDirent,
		unix.Socketpair,
		unix.UnixRights,
		unix.ParseSocketControlMessage,
		unix.Prctl,
		unix.Setrlimit, rl,
		unix.Exec,
	}
}

// Landlock is the second half of the same question, and the answer differs:
// x/sys/unix carries the structs, the access-right constants and the syscall
// numbers, but no function wrappers. The jail issues these three through
// unix.Syscall, which is what its seccomp half does regardless, so the missing
// wrappers cost an implementation detail and no design.
func Landlock() []any {
	var attr unix.LandlockRulesetAttr
	var path unix.LandlockPathBeneathAttr
	return []any{
		attr, path,
		unix.SYS_LANDLOCK_CREATE_RULESET,
		unix.SYS_LANDLOCK_ADD_RULE,
		unix.SYS_LANDLOCK_RESTRICT_SELF,
		unix.LANDLOCK_CREATE_RULESET_VERSION,
		unix.LANDLOCK_RULE_PATH_BENEATH,
		unix.LANDLOCK_ACCESS_FS_READ_FILE,
		unix.LANDLOCK_ACCESS_FS_WRITE_FILE,
		unix.LANDLOCK_ACCESS_FS_READ_DIR,
		unix.LANDLOCK_ACCESS_FS_REFER,
		unix.LANDLOCK_ACCESS_FS_TRUNCATE,
		unix.SYS_SECCOMP,
		unix.Syscall,
	}
}

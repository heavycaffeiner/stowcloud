//go:build linux

package unixprobe

import "golang.org/x/sys/unix"

// Syscalls returns the wrappers the path layer, the upload path and the
// worker socket resolve through. Referencing each as a value is enough: a
// rename, a removal, or a changed signature upstream fails this build.
func Syscalls() []any {
	var (
		openHow unix.OpenHow
		statx   unix.Statx_t
		statfs  unix.Statfs_t
		rlimit  unix.Rlimit
	)
	return []any{
		unix.Openat2, openHow,
		unix.Statx, statx,
		unix.Renameat2,
		unix.CopyFileRange,
		unix.CloseRange,
		unix.Fstatfs, statfs,
		unix.Getdents,
		unix.ParseDirent,
		unix.Socketpair,
		unix.UnixRights,
		unix.ParseSocketControlMessage,
		unix.Prctl,
		unix.Setrlimit, rlimit,
		unix.Exec,
	}
}

// Landlock returns the second half of the same probe. x/sys/unix carries the
// Landlock structs, access-right constants, and syscall numbers, but no
// function wrappers for them; the jail issues these three through
// unix.Syscall directly, which is what its seccomp half does regardless, so
// the missing wrappers cost an implementation detail rather than a design.
func Landlock() []any {
	var rulesetAttr unix.LandlockRulesetAttr
	var pathBeneath unix.LandlockPathBeneathAttr
	return []any{
		rulesetAttr, pathBeneath,
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

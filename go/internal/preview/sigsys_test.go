//go:build linux

package preview_test

import (
	"debug/elf"
	"encoding/binary"
	"os"
	"path/filepath"
	"strings"
)

// refusedSyscall reports the syscall number a seccomp filter refused, read out
// of the core the kill produced.
//
// SIGSYS carries the number in si_syscall, and the kernel copies the whole
// siginfo into the core's NT_SIGINFO note. Go cannot install a signal handler
// that reads it: SECCOMP_RET_KILL_PROCESS gives the process no chance to run
// one, and a handler safe enough to run from a signal context needs cgo, which
// this build does not use.
//
// Best effort by design. Cores are off on many machines and the pattern is
// distribution-specific, so this returns "" and the caller reports what it
// already had. It exists because the number is the entire content of the bug
// report: without it a missing entry is found by guessing at a list of three
// hundred syscalls.
func refusedSyscall(exe string) string {
	core := findCore(exe)
	if core == "" {
		return ""
	}
	f, err := elf.Open(core)
	if err != nil {
		return ""
	}
	defer func() { _ = f.Close() }() //nolint:errcheck // a diagnostic path; the read already happened.

	for _, p := range f.Progs {
		if p.Type != elf.PT_NOTE {
			continue
		}
		data := make([]byte, p.Filesz)
		if _, rerr := p.ReadAt(data, 0); rerr != nil {
			continue
		}
		if n, ok := sigsysNumber(data); ok {
			return syscallName(n)
		}
	}
	return ""
}

// sigsysNumber walks ELF notes for NT_SIGINFO and pulls si_syscall out of it.
//
// The siginfo layout here is the kernel's _sigsys arm: signo, errno and code
// are three ints, then a pointer to the calling instruction, then the syscall
// number. That puts it at offset 24 on a 64-bit target, after the eight bytes
// of padding the union's alignment adds.
func sigsysNumber(notes []byte) (int32, bool) {
	const ntSiginfo = 0x53494749
	for len(notes) >= 12 {
		nameSize := binary.LittleEndian.Uint32(notes[0:])
		descSize := binary.LittleEndian.Uint32(notes[4:])
		noteType := binary.LittleEndian.Uint32(notes[8:])

		head := 12 + align4(nameSize)
		body := align4(descSize)
		if uint64(head)+uint64(body) > uint64(len(notes)) {
			return 0, false
		}
		desc := notes[head : head+descSize]
		if noteType == ntSiginfo && len(desc) >= 28 {
			return int32(binary.LittleEndian.Uint32(desc[24:])), true //nolint:gosec // a signed field read back at its own width.
		}
		notes = notes[head+body:]
	}
	return 0, false
}

func align4(n uint32) uint32 { return (n + 3) &^ 3 }

// findCore looks where the common core patterns put one. Nothing here is
// portable, and none of it has to be: a machine that stores cores somewhere
// else reports no number, which is where this started.
func findCore(exe string) string {
	base := filepath.Base(exe)
	for _, dir := range []string{"/var/lib/systemd/coredump", "/tmp", "."} {
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		newest, newestTime := "", int64(0)
		for _, e := range entries {
			name := e.Name()
			if !strings.Contains(name, base) || !strings.Contains(name, "core") {
				continue
			}
			// Compressed cores are what systemd writes by default, and
			// decompressing one needs a tool this cannot assume.
			if strings.HasSuffix(name, ".zst") || strings.HasSuffix(name, ".lz4") {
				continue
			}
			info, ierr := e.Info()
			if ierr != nil {
				continue
			}
			if t := info.ModTime().UnixNano(); t > newestTime {
				newest, newestTime = filepath.Join(dir, name), t
			}
		}
		if newest != "" {
			return newest
		}
	}
	return ""
}

// syscallName turns a number into something a reader can act on. Only the
// calls a jailed decoder plausibly reaches are here: a full table would be
// three hundred lines to name one number that is nearly always in this set.
func syscallName(n int32) string {
	names := map[int32]string{
		9: "mmap", 10: "mprotect", 11: "munmap", 13: "rt_sigaction",
		14: "rt_sigprocmask", 24: "sched_yield", 25: "mremap", 28: "madvise",
		39: "getpid", 56: "clone", 131: "sigaltstack", 202: "futex",
		213: "epoll_create", 232: "epoll_wait", 233: "epoll_ctl",
		234: "tgkill", 273: "set_robust_list", 281: "epoll_pwait",
		290: "eventfd2", 291: "epoll_create1", 302: "prlimit64",
		318: "getrandom", 334: "rseq", 435: "clone3", 441: "epoll_pwait2",
	}
	if name, ok := names[n]; ok {
		return name + " (" + itoa(n) + ")"
	}
	return "syscall " + itoa(n)
}

func itoa(n int32) string {
	if n == 0 {
		return "0"
	}
	var buf [12]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[i:])
}

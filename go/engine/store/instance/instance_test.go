package instance_test

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/heavycaffeiner/stowcloud/go/engine/store/instance"
)

// take acquires the lock and fails the test if it could not.
func take(t *testing.T, dir string) *instance.Lock {
	t.Helper()
	l, err := instance.Take(dir)
	if err != nil {
		t.Fatalf("taking the lock: %v", err)
	}
	t.Cleanup(func() {
		if rerr := l.Release(); rerr != nil {
			t.Errorf("releasing: %v", rerr)
		}
	})
	return l
}

// The whole point: a second owner of one data directory is refused.
//
// The lock is on the open file description rather than the process, so a
// second call here contends with the first exactly as another process would.
// That is what makes this testable without spawning one.
func TestASecondOwnerIsRefused(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()

	take(t, dir)

	second, err := instance.Take(dir)
	if err == nil {
		if rerr := second.Release(); rerr != nil {
			t.Errorf("releasing: %v", rerr)
		}
		t.Fatal("two processes took the same data directory")
	}
	if !errors.Is(err, instance.ErrInUse) {
		t.Errorf("the refusal is %v, want ErrInUse", err)
	}
}

// Releasing hands the directory to the next owner. Without this a restart
// after a clean shutdown would be refused by the process that just exited.
func TestReleasingLetsTheNextOwnerIn(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()

	first, err := instance.Take(dir)
	if err != nil {
		t.Fatalf("taking the lock: %v", err)
	}
	if rerr := first.Release(); rerr != nil {
		t.Fatalf("releasing: %v", rerr)
	}

	take(t, dir)
}

// A lock file left behind by a crash blocks nothing.
//
// Nothing is written into it and nothing reads it: the only state is the
// kernel lock on the descriptor, which the kernel drops however the process
// exits. A stale file that refused the next start would turn every crash into
// manual cleanup.
func TestAFileLeftByACrashDoesNotBlock(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()

	path := filepath.Join(dir, instance.LockFile)
	if err := os.WriteFile(path, []byte("12345"), 0o600); err != nil {
		t.Fatalf("planting the stale file: %v", err)
	}

	take(t, dir)
}

// Release is idempotent, so a deferred release after an explicit one is not an
// error. Two closes of one descriptor is the second one failing.
func TestReleasingTwiceIsNotAnError(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()

	l, err := instance.Take(dir)
	if err != nil {
		t.Fatalf("taking the lock: %v", err)
	}
	if rerr := l.Release(); rerr != nil {
		t.Fatalf("the first release: %v", rerr)
	}
	if rerr := l.Release(); rerr != nil {
		t.Errorf("the second release: %v", rerr)
	}
}

// A nil lock releases cleanly, so a failed Take does not force the caller to
// guard its own cleanup path.
func TestANilLockReleasesCleanly(t *testing.T) {
	t.Parallel()
	var l *instance.Lock
	if err := l.Release(); err != nil {
		t.Errorf("releasing a nil lock: %v", err)
	}
}

// Two directories are independent. Locking one must not lock the machine.
func TestTwoDirectoriesAreIndependent(t *testing.T) {
	t.Parallel()
	take(t, t.TempDir())
	take(t, t.TempDir())
}

// The lock file survives a release. Removing it would let a second process
// create and lock a fresh one while a third still holds this, which is two
// owners each believing they are alone.
func TestTheFileSurvivesARelease(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()

	l, err := instance.Take(dir)
	if err != nil {
		t.Fatalf("taking the lock: %v", err)
	}
	if rerr := l.Release(); rerr != nil {
		t.Fatalf("releasing: %v", rerr)
	}

	if _, serr := os.Stat(filepath.Join(dir, instance.LockFile)); serr != nil {
		t.Errorf("the lock file was removed on release: %v", serr)
	}
}

// A directory that does not exist is a plain failure rather than a silent
// success that locks nothing.
func TestAMissingDirectoryIsRefused(t *testing.T) {
	t.Parallel()

	l, err := instance.Take(filepath.Join(t.TempDir(), "absent"))
	if err == nil {
		if rerr := l.Release(); rerr != nil {
			t.Errorf("releasing: %v", rerr)
		}
		t.Fatal("locking a missing directory succeeded")
	}
}

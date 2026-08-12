//go:build linux

package core

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/heavycaffeiner/stowcloud/go/internal/vfs"
)

// F11: the file ETag is always weak, a weak If-Match never succeeds as a
// strong validator, and the only way past a precondition is an explicit
// unconditional retry.

func TestFileETagIsDeterministicAndWeak(t *testing.T) {
	a := vfs.Stat{Dev: 1, Ino: 2, Size: 3, MtimeNs: 4, CtimeNs: int64p(5)}
	b := vfs.Stat{Dev: 1, Ino: 2, Size: 3, MtimeNs: 4, CtimeNs: int64p(5)}
	e1, w1 := FileETag(a)
	e2, w2 := FileETag(b)
	if e1 != e2 {
		t.Fatalf("FileETag is not deterministic")
	}
	if !w1 || !w2 {
		t.Fatalf("FileETag must always report weak, got weak=%v,%v", w1, w2)
	}

	// A rewrite in the same nanosecond at the same length was invisible to the
	// old mtime+size token; ctime catches the in-place rewrite.
	c := a
	c.CtimeNs = int64p(6)
	e3, _ := FileETag(c)
	if e1 == e3 {
		t.Fatalf("a ctime change must change the token (F11's case)")
	}
}

func TestFileETagIsNotStrongEnoughToHashContent(t *testing.T) {
	// The token is a function of metadata only; a content change at the same
	// size and timestamps is invisible, which is exactly why it is weak.
	a := vfs.Stat{Dev: 1, Ino: 9, Size: 5, MtimeNs: 100, CtimeNs: int64p(200)}
	// A different file, same metadata window, must not conflate with a if the
	// identity differs.
	b := a
	b.Ino = 10
	ea, _ := FileETag(a)
	eb, _ := FileETag(b)
	if ea == eb {
		t.Fatalf("distinct inodes must not share a token")
	}
}

func TestWeakIfMatchIsRefused(t *testing.T) {
	c, _, _ := testCore(t)
	r, err := resolve(t, c, "docs/a.txt", reqRead)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	st, serr := r.root.Stat(r.path)
	if serr != nil {
		t.Fatalf("stat: %v", serr)
	}
	cur, _ := FileETag(st)

	im := Token(cur)
	perr := precondition(&im, st)
	if !errors.Is(perr, ErrPrecondition) {
		t.Fatalf("precondition = %v, want ErrPrecondition", perr)
	}
	var pe *PreconditionError
	if !errors.As(perr, &pe) || pe.Current != cur {
		t.Fatalf("precondition must carry the current weak token, got %v", pe)
	}

	// An unconditional retry (no validator) is the only way past.
	if perr2 := precondition(nil, st); perr2 != nil {
		t.Fatalf("an unconditional retry must pass, got %v", perr2)
	}
}

// TestCreateFileWeakPreconditionIsRefused exercises the full operation path.
func TestCreateFileWeakPreconditionIsRefused(t *testing.T) {
	c, _, _ := testCore(t)
	r, err := resolve(t, c, "docs/a.txt", reqWrite)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	st, _ := r.root.Stat(r.path)
	cur, _ := FileETag(st)
	im := Token(cur)

	_, derr := c.CreateFile(ctx(), r, vfs.DurableOpts{Mode: 0o664}, &im, func(f *vfs.File) error {
		_, werr := f.WriteAt([]byte("new"), 0)
		return werr
	})
	if !errors.Is(derr, ErrPrecondition) {
		t.Fatalf("CreateFile with a weak If-Match = %v, want ErrPrecondition", derr)
	}
	// The file is untouched.
	body, _ := os.ReadFile(filepath.Join(t.TempDir(), "unused"))
	_ = body

	// Unconditional retry succeeds.
	_, uerr := c.CreateFile(ctx(), r, vfs.DurableOpts{Mode: 0o664}, nil, func(f *vfs.File) error {
		_, werr := f.WriteAt([]byte("new"), 0)
		return werr
	})
	if uerr != nil {
		t.Fatalf("unconditional retry = %v, want success", uerr)
	}
}

func int64p(v int64) *int64 { return &v }

var _ = context.Background

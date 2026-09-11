//go:build linux

package core

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/heavycaffeiner/stowcloud/go/engine/infra/vfs"
	"github.com/heavycaffeiner/stowcloud/go/engine/service/acl"
	"github.com/heavycaffeiner/stowcloud/go/engine/store/journal"
	"github.com/heavycaffeiner/stowcloud/go/engine/store/state"
)

// twoShares is a caller holding everything on two separate shares, which is
// what makes a transfer cross a boundary without needing a real mount.
func twoShares(t *testing.T) (c *Core, st *state.DB, srcHost, dstHost string, src, dst Resolved) {
	t.Helper()
	c, st = newCore(t)
	seedUser(t, st, 1, "ada")
	_, srcHost = share(t, c, 10, "documents")
	_, dstHost = share(t, c, 11, "archive")
	grantAt(t, c, st, 1, 10, "", "Documents", allPerms)
	grantAt(t, c, st, 1, 11, "", "Archive", allPerms)

	var err error
	src, err = c.Resolve(1, vpath(t, "Documents"), acl.Read)
	if err != nil {
		t.Fatalf("resolving the source root: %v", err)
	}
	dst, err = c.Resolve(1, vpath(t, "Archive"), acl.Read)
	if err != nil {
		t.Fatalf("resolving the destination root: %v", err)
	}
	return c, st, srcHost, dstHost, src, dst
}

type failedRestoreRoot struct {
	vfs.Root
	err error
}

func (r failedRestoreRoot) Rename(_, _ vfs.SafePath, _ bool) error {
	return r.err
}

func TestFailedBackupRestorationReportsTheRecoveryPath(t *testing.T) {
	t.Parallel()
	cause := errors.New("preparation refused")
	rollback := errors.New("restore refused")
	dest := safe(t, "destination")
	backup, err := vfs.RootPath().JoinControl(".scmeta-replace-recovery")
	if err != nil {
		t.Fatalf("building backup path: %v", err)
	}

	err = restoreTransferBackup(failedRestoreRoot{err: rollback}, dest, backup, cause)
	var partial *PartialTransferError
	if !errors.As(err, &partial) {
		t.Fatalf("restore failure returned %T, want PartialTransferError", err)
	}
	if !partial.Destination.Equal(dest) || !partial.Backup.Equal(backup) {
		t.Fatalf("recovery paths are destination=%q backup=%q", partial.Destination, partial.Backup)
	}
	if !errors.Is(err, cause) || !errors.Is(err, rollback) {
		t.Fatalf("partial error does not preserve cause and rollback: %v", err)
	}
}

// at re-paths a resolution without spending a second gate, which is how a
// test names a child of an already-resolved root. The permission set travels
// with it, exactly as the conflict policy's re-pathing does.
func at(t *testing.T, r Resolved, rel string) Resolved {
	t.Helper()
	p := r.path
	if rel != "" {
		for _, comp := range safe(t, rel).Components() {
			next, err := p.JoinExisting(comp)
			if err != nil {
				t.Fatalf("joining %q: %v", rel, err)
			}
			p = next
		}
	}
	return Resolved{user: r.user, share: r.share, root: r.root, path: p, perms: r.perms}
}

// deviceOf is the source device number a crossesDevice call needs.
func deviceOf(t *testing.T, r Resolved) uint64 {
	t.Helper()
	st, err := r.root.Stat(r.path)
	if err != nil {
		t.Fatalf("stat for the device number: %v", err)
	}
	return st.Dev
}

func TestParseOnConflictMapsEverySpelling(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		in   string
		want OnConflict
	}{
		{"", ConflictFail},
		{"fail", ConflictFail},
		{"  FAIL  ", ConflictFail},
		{"rename", ConflictRename},
		{"Rename", ConflictRename},
		{"overwrite", ConflictOverwrite},
		{"  Overwrite", ConflictOverwrite},
		{"skip", ConflictSkip},
		{"\tSKIP\n", ConflictSkip},
	} {
		got, ok := ParseOnConflict(tc.in)
		if !ok {
			t.Fatalf("ParseOnConflict(%q) reported not-ok", tc.in)
		}
		if got != tc.want {
			t.Fatalf("ParseOnConflict(%q) = %d, want %d", tc.in, got, tc.want)
		}
	}
}

func TestParseOnConflictRefusesAnUnknownSpelling(t *testing.T) {
	t.Parallel()
	// The false return is what the caller must act on. Returning fail as the
	// value is incidental; silently applying it is the bug this prevents.
	got, ok := ParseOnConflict("clobber")
	if ok {
		t.Fatal("ParseOnConflict accepted a policy this build does not have")
	}
	if got != ConflictFail {
		t.Fatalf("the refused parse returned policy %d, want the fail default", got)
	}
}

func TestMoveWithinAShareIsAPlainRename(t *testing.T) {
	t.Parallel()
	c, _, host, root := writable(t)
	ctx := context.Background()
	j := attachJournal(t, c)
	writeFile(t, host, "note.txt", "body")

	from := at(t, root, "note.txt")
	to := at(t, root, "moved.txt")
	res, err := c.Move(ctx, from, to, MoveOpts{})
	if err != nil {
		t.Fatalf("Move: %v", err)
	}
	if !res.Moved || res.WillCopy || res.Skipped {
		t.Fatalf("Move returned %+v, want a plain rename", res)
	}
	if !res.Created.Equal(to.path) {
		t.Fatalf("Move landed at %q, want %q", res.Created, to.path)
	}
	if got := readHost(t, host, "moved.txt"); got != "body" {
		t.Fatalf("the moved file holds %q, want %q", got, "body")
	}
	if _, err = os.Stat(filepath.Join(host, "note.txt")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("the source name survived a rename")
	}

	rows, err := j.Recent(ctx, 1, 10)
	if err != nil {
		t.Fatalf("reading the journal: %v", err)
	}
	if len(rows) != 1 || rows[0].Op != journal.OpMove {
		t.Fatalf("the journal holds %+v, want one move row", rows)
	}
}

func TestMoveRefusesAShareRootAtEitherEnd(t *testing.T) {
	t.Parallel()
	c, _, host, root := writable(t)
	ctx := context.Background()
	writeFile(t, host, "note.txt", "body")

	if _, err := c.Move(ctx, root, at(t, root, "note.txt"), MoveOpts{}); !errors.Is(err, ErrDenied) {
		t.Fatalf("moving a share root returned %v, want ErrDenied", err)
	}
	if _, err := c.Move(ctx, at(t, root, "note.txt"), root, MoveOpts{}); !errors.Is(err, ErrDenied) {
		t.Fatalf("moving onto a share root returned %v, want ErrDenied", err)
	}
}

func TestMoveOntoItselfIsANoOpSuccess(t *testing.T) {
	t.Parallel()
	c, _, host, root := writable(t)
	writeFile(t, host, "note.txt", "body")

	from := at(t, root, "note.txt")
	res, err := c.Move(context.Background(), from, from, MoveOpts{})
	if err != nil {
		t.Fatalf("moving a path onto itself: %v", err)
	}
	if res.Moved || res.WillCopy || res.Skipped {
		t.Fatalf("the no-op returned %+v, want an untouched result", res)
	}
	if got := readHost(t, host, "note.txt"); got != "body" {
		t.Fatalf("the no-op changed the file to %q", got)
	}
}

func TestMoveFailsOnATakenDestination(t *testing.T) {
	t.Parallel()
	c, _, host, root := writable(t)
	writeFile(t, host, "a.txt", "source")
	writeFile(t, host, "b.txt", "taken")

	_, err := c.Move(context.Background(), at(t, root, "a.txt"), at(t, root, "b.txt"), MoveOpts{})
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("the fail policy returned %v, want ErrConflict", err)
	}
	if got := readHost(t, host, "b.txt"); got != "taken" {
		t.Fatalf("the refused move changed the destination to %q", got)
	}
}

func TestMoveSkipsATakenDestinationWithoutWriting(t *testing.T) {
	t.Parallel()
	c, _, host, root := writable(t)
	writeFile(t, host, "a.txt", "source")
	writeFile(t, host, "b.txt", "taken")

	res, err := c.Move(context.Background(), at(t, root, "a.txt"), at(t, root, "b.txt"),
		MoveOpts{OnConflict: ConflictSkip})
	if err != nil {
		t.Fatalf("the skip policy: %v", err)
	}
	if !res.Skipped || res.Moved || res.WillCopy {
		t.Fatalf("the skip returned %+v, want Skipped alone", res)
	}
	if got := readHost(t, host, "b.txt"); got != "taken" {
		t.Fatalf("the skip wrote %q to the destination", got)
	}
	if got := readHost(t, host, "a.txt"); got != "source" {
		t.Fatalf("the skip disturbed the source, now %q", got)
	}
}

func TestMoveKeepsBothUnderTheRenamePolicy(t *testing.T) {
	t.Parallel()
	c, _, host, root := writable(t)
	writeFile(t, host, "a.txt", "source")
	writeFile(t, host, "b.txt", "taken")

	res, err := c.Move(context.Background(), at(t, root, "a.txt"), at(t, root, "b.txt"),
		MoveOpts{OnConflict: ConflictRename})
	if err != nil {
		t.Fatalf("the rename policy: %v", err)
	}
	// Created is the suffixed name, so the caller reports back where the
	// entry actually landed rather than echoing its own request.
	if res.Created.Name() != "b (2).txt" {
		t.Fatalf("the rename landed at %q, want \"b (2).txt\"", res.Created.Name())
	}
	if got := readHost(t, host, "b (2).txt"); got != "source" {
		t.Fatalf("the renamed destination holds %q", got)
	}
	if got := readHost(t, host, "b.txt"); got != "taken" {
		t.Fatal("the rename overwrote the original destination")
	}
}

func TestMoveReplacesAFileUnderTheOverwritePolicy(t *testing.T) {
	t.Parallel()
	c, _, host, root := writable(t)
	writeFile(t, host, "a.txt", "source")
	writeFile(t, host, "b.txt", "taken")

	res, err := c.Move(context.Background(), at(t, root, "a.txt"), at(t, root, "b.txt"),
		MoveOpts{OnConflict: ConflictOverwrite})
	if err != nil {
		t.Fatalf("the overwrite policy: %v", err)
	}
	if !res.Moved {
		t.Fatalf("the overwrite returned %+v, want a rename", res)
	}
	if got := readHost(t, host, "b.txt"); got != "source" {
		t.Fatalf("the overwritten destination holds %q, want the source content", got)
	}
}

func TestOverwriteOntoAFreeNameStillRefusesAClobber(t *testing.T) {
	t.Parallel()
	c, _, host, root := writable(t)
	writeFile(t, host, "a.txt", "source")

	// The destination is free, so nothing is being replaced and the rename
	// must keep its no-replace flag: a race that fills the name between the
	// check and the rename is a refusal, never a clobber.
	res, err := c.Move(context.Background(), at(t, root, "a.txt"), at(t, root, "b.txt"),
		MoveOpts{OnConflict: ConflictOverwrite})
	if err != nil {
		t.Fatalf("overwriting onto a free name: %v", err)
	}
	if !res.Moved {
		t.Fatalf("the move returned %+v, want a plain rename", res)
	}
	if got := readHost(t, host, "b.txt"); got != "source" {
		t.Fatalf("the destination holds %q", got)
	}
}

func TestOverwriteOntoANonEmptyDirectoryReplacesRatherThanMerges(t *testing.T) {
	t.Parallel()
	c, _, host, root := writable(t)
	ctx := context.Background()
	if err := os.MkdirAll(filepath.Join(host, "src"), 0o755); err != nil {
		t.Fatalf("building the source: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(host, "dst"), 0o755); err != nil {
		t.Fatalf("building the destination: %v", err)
	}
	writeFile(t, host, "src/kept.txt", "from source")
	writeFile(t, host, "dst/stale.txt", "only in the destination")

	// The kernel answers ENOTEMPTY to a rename over a non-empty directory,
	// which is what the pre-delete exists to get past.
	if _, err := c.Move(ctx, at(t, root, "src"), at(t, root, "dst"),
		MoveOpts{OnConflict: ConflictOverwrite}); err != nil {
		t.Fatalf("overwriting a non-empty directory: %v", err)
	}
	if got := readHost(t, host, "dst/kept.txt"); got != "from source" {
		t.Fatalf("the replaced directory holds %q", got)
	}
	if _, err := os.Stat(filepath.Join(host, "dst/stale.txt")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("a member only the destination had survived, so the move merged rather than replaced")
	}
}

func TestMoveReplacementPreservesActiveUploadControls(t *testing.T) {
	t.Parallel()
	c, _, host, root := writable(t)
	if err := os.MkdirAll(filepath.Join(host, "src"), 0o755); err != nil {
		t.Fatalf("building the source: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(host, "dst"), 0o755); err != nil {
		t.Fatalf("building the destination: %v", err)
	}
	writeFile(t, host, "src/new.txt", "new")
	writeFile(t, host, "dst/old.txt", "old")
	writeFile(t, host, "dst/.scpart-live", "upload in progress")

	_, err := c.Move(context.Background(), at(t, root, "src"), at(t, root, "dst"),
		MoveOpts{OnConflict: ConflictOverwrite})
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("replacement over an active upload returned %v, want ErrConflict", err)
	}
	if got := readHost(t, host, "src/new.txt"); got != "new" {
		t.Fatalf("the rolled-back source holds %q", got)
	}
	if got := readHost(t, host, "dst/old.txt"); got != "old" {
		t.Fatalf("the rolled-back destination holds %q", got)
	}
	if got := readHost(t, host, "dst/.scpart-live"); got != "upload in progress" {
		t.Fatalf("the active upload control holds %q", got)
	}
}

func TestMoveWithAValidatorIsRefusedWithTheCurrentToken(t *testing.T) {
	t.Parallel()
	c, _, host, root := writable(t)
	writeFile(t, host, "a.txt", "source")
	writeFile(t, host, "b.txt", "taken")

	to := at(t, root, "b.txt")
	st, err := to.root.Stat(to.path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	want, _ := FileETag(st)

	token := Token("anything")
	_, err = c.Move(context.Background(), at(t, root, "a.txt"), to,
		MoveOpts{OnConflict: ConflictOverwrite, IfMatch: &token})
	if !IsPrecondition(err) {
		t.Fatalf("a supplied validator returned %v, want ErrPrecondition", err)
	}
	var perr *PreconditionError
	if !errors.As(err, &perr) || perr.Current != want {
		t.Fatalf("the refusal carried %+v, want the current token %q", perr, want)
	}
	if got := readHost(t, host, "b.txt"); got != "taken" {
		t.Fatalf("the refused overwrite still wrote %q", got)
	}
}

func TestTheLegacyOverwriteFieldWinsOverThePolicy(t *testing.T) {
	t.Parallel()
	c, _, host, root := writable(t)
	writeFile(t, host, "a.txt", "source")
	writeFile(t, host, "b.txt", "taken")

	// Overwrite true alongside the zero policy, which is fail. The fold must
	// pick overwrite, or an older caller silently gets a conflict.
	if _, err := c.Move(context.Background(), at(t, root, "a.txt"), at(t, root, "b.txt"),
		MoveOpts{Overwrite: true}); err != nil {
		t.Fatalf("the legacy overwrite field: %v", err)
	}
	if got := readHost(t, host, "b.txt"); got != "source" {
		t.Fatalf("the destination holds %q, want the source content", got)
	}
}

func TestACrossShareMoveCopiesThenDeletes(t *testing.T) {
	t.Parallel()
	c, _, srcHost, dstHost, src, dst := twoShares(t)
	ctx := context.Background()
	if err := os.MkdirAll(filepath.Join(srcHost, "tree/inner"), 0o755); err != nil {
		t.Fatalf("building the tree: %v", err)
	}
	writeFile(t, srcHost, "tree/top.txt", "top")
	writeFile(t, srcHost, "tree/inner/leaf.txt", "leaf")

	res, err := c.Move(ctx, at(t, src, "tree"), at(t, dst, "tree"), MoveOpts{})
	if err != nil {
		t.Fatalf("the cross-share move: %v", err)
	}
	if !res.WillCopy || res.Moved {
		t.Fatalf("the cross-share move returned %+v, want WillCopy", res)
	}
	if got := readHost(t, dstHost, "tree/top.txt"); got != "top" {
		t.Fatalf("the copied top file holds %q", got)
	}
	if got := readHost(t, dstHost, "tree/inner/leaf.txt"); got != "leaf" {
		t.Fatalf("the copied leaf holds %q", got)
	}
	if _, err := os.Stat(filepath.Join(srcHost, "tree")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("the source tree survived a completed cross-share move")
	}
}

func TestACrossShareMoveNeedsMoveRatherThanDelete(t *testing.T) {
	t.Parallel()
	c, _, srcHost, dstHost, src, dst := twoShares(t)
	writeFile(t, srcHost, "note.txt", "body")
	from := at(t, src, "note.txt").WithMask(acl.Read | acl.Move)

	if _, err := c.Move(context.Background(), from, at(t, dst, "note.txt"), MoveOpts{}); err != nil {
		t.Fatalf("moving with Read and Move: %v", err)
	}
	if got := readHost(t, dstHost, "note.txt"); got != "body" {
		t.Fatalf("the destination holds %q", got)
	}
	if _, err := os.Stat(filepath.Join(srcHost, "note.txt")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("the source survived a completed move")
	}
}

func TestACrossShareDirectoryMoveUsesMoveForSourceCleanup(t *testing.T) {
	t.Parallel()
	c, _, srcHost, dstHost, src, dst := twoShares(t)
	ctx := context.Background()
	if err := os.MkdirAll(filepath.Join(srcHost, "tree/inner"), 0o755); err != nil {
		t.Fatalf("building the source tree: %v", err)
	}
	writeFile(t, srcHost, "tree/top.txt", "top")
	writeFile(t, srcHost, "tree/inner/leaf.txt", "leaf")

	// Read and Move are enough for a cross-device directory move. Delete is
	// intentionally absent because Move authorizes removing the source.
	from := at(t, src, "tree").WithMask(acl.Read | acl.Move)
	if !from.Has(acl.Read|acl.Move) || from.Has(acl.Delete) {
		t.Fatalf("the move test capability has permissions %s, want Read and Move without Delete", from.Perms())
	}
	res, err := c.Move(ctx, from, at(t, dst, "tree"), MoveOpts{})
	if err != nil {
		t.Fatalf("the Move-only cross-share directory move: %v", err)
	}
	if !res.WillCopy || res.Moved {
		t.Fatalf("the Move-only cross-share move returned %+v, want WillCopy", res)
	}
	if got := readHost(t, dstHost, "tree/inner/leaf.txt"); got != "leaf" {
		t.Fatalf("the copied leaf holds %q", got)
	}
	if _, err := os.Stat(filepath.Join(srcHost, "tree")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("the source tree survived a Move-only cross-share move")
	}
}

func denyMoveAt(t *testing.T, c *Core, st *state.DB, user int64, share ShareID, subpath string) {
	t.Helper()
	holder := user
	if _, err := st.PersistGrant(context.Background(), state.GrantRow{
		User:    &holder,
		Share:   int64(share),
		Subpath: subpath,
		Allow:   uint16(acl.Read),
		Deny:    uint16(acl.Move),
		Inherit: true,
	}, 0); err != nil {
		t.Fatalf("persisting the move deny grant: %v", err)
	}
	if err := c.ReloadGrants(context.Background()); err != nil {
		t.Fatalf("reloading the move deny grant: %v", err)
	}
}

func TestACrossShareMovePreflightsMoveAuthorityBeforeStaging(t *testing.T) {
	t.Parallel()
	c, st, srcHost, dstHost, src, dst := twoShares(t)
	if err := os.MkdirAll(filepath.Join(srcHost, "tree/blocked"), 0o755); err != nil {
		t.Fatalf("building the source tree: %v", err)
	}
	writeFile(t, srcHost, "tree/top.txt", "top")
	writeFile(t, srcHost, "tree/blocked/leaf.txt", "leaf")
	denyMoveAt(t, c, st, 1, 10, "tree/blocked")

	from := at(t, src, "tree").WithMask(acl.Read | acl.Move)
	_, err := c.Move(context.Background(), from, at(t, dst, "tree"), MoveOpts{})
	if !errors.Is(err, ErrDenied) {
		t.Fatalf("a denied source descendant returned %v, want ErrDenied", err)
	}
	if _, err := os.Stat(filepath.Join(dstHost, "tree")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("the denied move published a destination before authority preflight")
	}
	if got := readHost(t, srcHost, "tree/top.txt"); got != "top" {
		t.Fatalf("the denied move changed the source tree: %q", got)
	}
	if got := readHost(t, srcHost, "tree/blocked/leaf.txt"); got != "leaf" {
		t.Fatalf("the denied move changed the blocked source tree: %q", got)
	}
}

func TestACrossShareMoveReportsAFailedSourceDelete(t *testing.T) {
	t.Parallel()
	c, _, srcHost, dstHost, src, dst := twoShares(t)
	ctx := context.Background()
	if err := os.MkdirAll(filepath.Join(srcHost, "locked"), 0o755); err != nil {
		t.Fatalf("building the source: %v", err)
	}
	writeFile(t, srcHost, "locked/file.txt", "content")
	// A read-only parent is what makes the unlink fail after the copy has
	// already committed.
	if err := os.Chmod(filepath.Join(srcHost, "locked"), 0o555); err != nil {
		t.Fatalf("sealing the source: %v", err)
	}
	t.Cleanup(func() {
		if err := os.Chmod(filepath.Join(srcHost, "locked"), 0o755); err != nil {
			t.Errorf("unsealing the source: %v", err)
		}
	})

	_, err := c.Move(ctx, at(t, src, "locked"), at(t, dst, "locked"), MoveOpts{})
	if !errors.Is(err, ErrCrossShare) {
		t.Fatalf("a failed source delete returned %v, want ErrCrossShare", err)
	}
	// The caller is told a duplicate exists, so the destination must be there.
	if got := readHost(t, dstHost, "locked/file.txt"); got != "content" {
		t.Fatalf("the destination holds %q, want the copy to have survived", got)
	}
}

func TestCrossesDeviceAnswersPerShareAndPerParent(t *testing.T) {
	t.Parallel()
	_, _, srcHost, _, src, dst := twoShares(t)
	writeFile(t, srcHost, "note.txt", "body")

	from := at(t, src, "note.txt")
	dev := deviceOf(t, from)
	if crossesDevice(from, at(t, src, "other.txt"), dev) {
		t.Fatal("two paths in one share on one device were called cross-device")
	}
	// Two shares are two trees whatever the filesystem says.
	if !crossesDevice(from, at(t, dst, "note.txt"), dev) {
		t.Fatal("two shares were called same-device")
	}
	// Fail-closed on a destination whose device cannot be read: a copy
	// across a boundary that was not there is slow, a rename across one that
	// was is a failed move. A closed root is the reachable way to make the
	// probe fail without arranging a mount.
	sealed := at(t, src, "note.txt")
	if err := sealed.root.Close(); err != nil {
		t.Fatalf("closing the root: %v", err)
	}
	if !crossesDevice(from, sealed, dev) {
		t.Fatal("a destination parent whose device cannot be read was called same-device")
	}
}

func TestWouldCopyIsFailOpenOnAnUnstattableSource(t *testing.T) {
	t.Parallel()
	c, _, srcHost, _, src, dst := twoShares(t)
	writeFile(t, srcHost, "note.txt", "body")

	if c.WouldCopy(at(t, src, "note.txt"), at(t, src, "other.txt")) {
		t.Fatal("a same-device move was predicted as a copy")
	}
	if !c.WouldCopy(at(t, src, "note.txt"), at(t, dst, "note.txt")) {
		t.Fatal("a cross-share move was not predicted as a copy")
	}
	// The move itself reports what is wrong with a missing source; a
	// preflight that refuses is a picker that cannot open.
	if c.WouldCopy(at(t, src, "absent.txt"), at(t, dst, "absent.txt")) {
		t.Fatal("an unstattable source answered true rather than false")
	}
}

func TestCopyRecursiveCarriesTheTreeAndSkipsReservedNames(t *testing.T) {
	t.Parallel()
	c, _, srcHost, dstHost, src, dst := twoShares(t)
	ctx := context.Background()
	if err := os.MkdirAll(filepath.Join(srcHost, "tree/inner"), 0o755); err != nil {
		t.Fatalf("building the tree: %v", err)
	}
	writeFile(t, srcHost, "tree/top.txt", "top")
	writeFile(t, srcHost, "tree/inner/leaf.txt", "leaf")
	// A control name the upload engine could have left behind. HideReserved
	// is what keeps a part file in progress out of the copy.
	writeFile(t, srcHost, "tree/.scpart-abc", "partial")

	from := at(t, src, "tree")
	st, err := from.root.Stat(from.path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if err := c.copyRecursive(ctx, from, at(t, dst, "tree"), st, nil); err != nil {
		t.Fatalf("copyRecursive: %v", err)
	}

	if got := readHost(t, dstHost, "tree/top.txt"); got != "top" {
		t.Fatalf("the copied top file holds %q", got)
	}
	if got := readHost(t, dstHost, "tree/inner/leaf.txt"); got != "leaf" {
		t.Fatalf("the copied leaf holds %q", got)
	}
	if _, err := os.Stat(filepath.Join(dstHost, "tree/.scpart-abc")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("a reserved control name was copied")
	}
}

func TestCopyFileReplacesAnExistingDestination(t *testing.T) {
	t.Parallel()
	c, _, srcHost, dstHost, src, dst := twoShares(t)
	writeFile(t, srcHost, "note.txt", "new content")
	writeFile(t, dstHost, "note.txt", "old content")

	if err := c.copyFile(context.Background(),
		at(t, src, "note.txt"), at(t, dst, "note.txt")); err != nil {
		t.Fatalf("copyFile over an existing name: %v", err)
	}
	if got := readHost(t, dstHost, "note.txt"); got != "new content" {
		t.Fatalf("the replaced destination holds %q", got)
	}
}

func TestTheCancellationGateStopsTheWalkAtAnItemBoundary(t *testing.T) {
	t.Parallel()
	c, _, srcHost, dstHost, src, dst := twoShares(t)
	ctx := context.Background()
	if err := os.MkdirAll(filepath.Join(srcHost, "tree"), 0o755); err != nil {
		t.Fatalf("building the tree: %v", err)
	}
	writeFile(t, srcHost, "tree/a.txt", "a")
	writeFile(t, srcHost, "tree/b.txt", "b")

	from := at(t, src, "tree")
	st, err := from.root.Stat(from.path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	// True from the first poll, so the walk stops before the directory it
	// was asked for is created.
	err = c.copyRecursive(ctx, from, at(t, dst, "tree"), st, func() bool { return true })
	if !errors.Is(err, errOpCancelled) {
		t.Fatalf("a cancelled walk returned %v, want errOpCancelled", err)
	}
	if _, serr := os.Stat(filepath.Join(dstHost, "tree")); !errors.Is(serr, os.ErrNotExist) {
		t.Fatal("the walk wrote past the cancellation")
	}
}

func TestACopyChildThatVanishesUnderTheWalkIsSkipped(t *testing.T) {
	t.Parallel()
	c, _, srcHost, dstHost, src, dst := twoShares(t)
	ctx := context.Background()
	if err := os.MkdirAll(filepath.Join(srcHost, "tree"), 0o755); err != nil {
		t.Fatalf("building the tree: %v", err)
	}
	writeFile(t, srcHost, "tree/kept.txt", "kept")
	// A dangling symlink is a name ReadDir returns and Stat cannot answer,
	// which is the vanished-child case without a race to arrange.
	if err := os.Symlink(filepath.Join(srcHost, "gone"),
		filepath.Join(srcHost, "tree/dangling.txt")); err != nil {
		t.Fatalf("planting the dangling entry: %v", err)
	}

	from := at(t, src, "tree")
	st, err := from.root.Stat(from.path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if err := c.copyRecursive(ctx, from, at(t, dst, "tree"), st, nil); err != nil {
		t.Fatalf("a dangling child made the copy fatal: %v", err)
	}
	if got := readHost(t, dstHost, "tree/kept.txt"); got != "kept" {
		t.Fatalf("the surviving child holds %q", got)
	}
}

func TestRefuseSelfDescendantComparesComponentsNotBytes(t *testing.T) {
	t.Parallel()
	_, _, _, _, src, dst := twoShares(t)

	for _, tc := range []struct {
		name     string
		from, to Resolved
		refuse   bool
	}{
		{"onto itself", at(t, src, "a/b"), at(t, src, "a/b"), true},
		{"into a child", at(t, src, "a/b"), at(t, src, "a/b/c"), true},
		// The trap this rule exists to avoid: a string prefix check makes
		// "a/bc" look like a child of "a/b".
		{"onto a name-extending sibling", at(t, src, "a/b"), at(t, src, "a/bc"), false},
		{"onto a shallower path", at(t, src, "a/b"), at(t, src, "a"), false},
		{"across shares at the same path", at(t, src, "a/b"), at(t, dst, "a/b"), false},
	} {
		err := RefuseSelfDescendant(tc.from, tc.to)
		if tc.refuse && !errors.Is(err, ErrDenied) {
			t.Fatalf("%s: returned %v, want ErrDenied", tc.name, err)
		}
		if !tc.refuse && err != nil {
			t.Fatalf("%s: returned %v, want it allowed", tc.name, err)
		}
	}
}

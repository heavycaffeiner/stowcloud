//go:build linux

package core

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/heavycaffeiner/stowcloud/go/engine/infra/vfs"
	"github.com/heavycaffeiner/stowcloud/go/engine/kit/clock"
	"github.com/heavycaffeiner/stowcloud/go/engine/service/acl"
	"github.com/heavycaffeiner/stowcloud/go/engine/store/dbfile"
	"github.com/heavycaffeiner/stowcloud/go/engine/store/journal"
	"github.com/heavycaffeiner/stowcloud/go/engine/store/state"
)

// allPerms is every bit, so a mutation test resolves once and then exercises
// the operation rather than the gate. The refusal cases resolve their own
// narrower grants.
const allPerms = acl.Read | acl.Write | acl.Create | acl.Delete |
	acl.Rename | acl.Move | acl.Share | acl.Download

// writable is a share the caller may do anything on, resolved at its root.
func writable(t *testing.T) (c *Core, st *state.DB, host string, root Resolved) {
	t.Helper()
	c, st = newCore(t)
	seedUser(t, st, 1, "ada")
	_, host = share(t, c, 10, "documents")
	grantAt(t, c, st, 1, 10, "", "Documents", allPerms)

	root, err := c.Resolve(1, vpath(t, "Documents"), acl.Read)
	if err != nil {
		t.Fatalf("resolving the share root: %v", err)
	}
	return c, st, host, root
}

// attachJournal opens a real journal for the core, which is how the record
// tests read back what a mutation wrote.
func attachJournal(t *testing.T, c *Core) *journal.DB {
	t.Helper()
	f, err := dbfile.Open(context.Background(),
		journal.Spec(filepath.Join(t.TempDir(), "journal.db")))
	if err != nil {
		t.Fatalf("opening the journal: %v", err)
	}
	t.Cleanup(func() {
		if cerr := f.Close(); cerr != nil {
			t.Errorf("closing the journal: %v", cerr)
		}
	})
	j := journal.New(f, clock.System())
	c.journal = j
	return j
}

// countingSink records what the write paths charged, so the ledger rules are
// asserted without a user row's columns.
type countingSink struct {
	reserved []uint64
	released []int64
	refuse   bool
	fail     error
}

func (s *countingSink) Reserve(_ context.Context, _ int64, additional uint64) (bool, error) {
	if s.fail != nil {
		return false, s.fail
	}
	s.reserved = append(s.reserved, additional)
	return !s.refuse, nil
}

func (s *countingSink) Commit(context.Context, int64, uint64) error { return nil }

func (s *countingSink) Release(_ context.Context, _ int64, delta int64) error {
	if s.fail != nil {
		return s.fail
	}
	s.released = append(s.released, delta)
	return nil
}

func attachSink(t *testing.T, c *Core) *countingSink {
	t.Helper()
	s := &countingSink{}
	if err := c.AttachQuotaSink(s); err != nil {
		t.Fatalf("attaching the sink: %v", err)
	}
	return s
}

// under resolves a path beneath the writable share.
func under(t *testing.T, c *Core, path string, need acl.Perms) Resolved {
	t.Helper()
	r, err := c.Resolve(1, vpath(t, path), need)
	if err != nil {
		t.Fatalf("resolving %q: %v", path, err)
	}
	return r
}

func writeAll(content string) func(*vfs.File) error {
	return func(f *vfs.File) error {
		_, err := f.WriteAt([]byte(content), 0)
		return err
	}
}

func mustCreate(t *testing.T, c *Core, r Resolved, content string) Entry {
	t.Helper()
	e, err := c.CreateFile(context.Background(), r,
		vfs.DurableOpts{Mode: 0o644}, nil, writeAll(content))
	if err != nil {
		t.Fatalf("CreateFile: %v", err)
	}
	return e
}

func TestPreconditionRefusesEveryValidator(t *testing.T) {
	c, _, host, _ := writable(t)
	writeFile(t, host, "readme.txt", "hello")
	r := under(t, c, "Documents/readme.txt", acl.Read)

	st, err := r.Root().Stat(r.Path())
	if err != nil {
		t.Fatalf("stating the file: %v", err)
	}
	if perr := precondition(nil, st); perr != nil {
		t.Fatalf("a nil validator was refused: %v", perr)
	}

	want, _ := FileETag(st)
	for _, tok := range []Token{"", "anything", Token(want)} {
		perr := precondition(&tok, st)
		if !IsPrecondition(perr) {
			t.Fatalf("precondition(%q) = %v, want ErrPrecondition", tok, perr)
		}
		var pe *PreconditionError
		if !errors.As(perr, &pe) || pe.Current != want {
			t.Fatalf("the refusal for %q carries %+v, want the current token %q", tok, pe, want)
		}
	}
}

func TestAValidatorAgainstAMissingTargetCarriesNoToken(t *testing.T) {
	c, _, _, _ := writable(t)
	r := under(t, c, "Documents/absent.txt", acl.Write)

	tok := Token("whatever")
	_, err := c.CreateFile(context.Background(), r,
		vfs.DurableOpts{Mode: 0o644}, &tok, writeAll("x"))
	var pe *PreconditionError
	if !errors.As(err, &pe) || pe.Current != "" {
		t.Fatalf("creating a missing file under a validator = %v, want an empty current token", err)
	}
	if !IsPrecondition(err) {
		t.Fatalf("the refusal does not unwrap to ErrPrecondition: %v", err)
	}

	// The unconditional retry is the way past, and it is the only one.
	mustCreate(t, c, r, "x")
}

func TestTheUnconditionalRetryIsTheWayPast(t *testing.T) {
	c, _, host, _ := writable(t)
	writeFile(t, host, "notes.txt", "old")
	r := under(t, c, "Documents/notes.txt", acl.Write)

	tok := Token("v1")
	if _, err := c.CreateFile(context.Background(), r,
		vfs.DurableOpts{Mode: 0o644}, &tok, writeAll("new")); !IsPrecondition(err) {
		t.Fatalf("a validated replace = %v, want ErrPrecondition", err)
	}
	if got := readHost(t, host, "notes.txt"); got != "old" {
		t.Fatalf("the refused write changed the file to %q", got)
	}
	mustCreate(t, c, r, "new")
	if got := readHost(t, host, "notes.txt"); got != "new" {
		t.Fatalf("the retry left %q", got)
	}
}

func readHost(t *testing.T, host, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(host, name))
	if err != nil {
		t.Fatalf("reading %q: %v", name, err)
	}
	return string(b)
}

func TestMkdirCreatesAndRefuses(t *testing.T) {
	c, st, host, root := writable(t)
	dir := under(t, c, "Documents/reports", acl.Create)

	e, err := c.Mkdir(context.Background(), dir)
	if err != nil {
		t.Fatalf("Mkdir: %v", err)
	}
	if e.Kind != vfs.KindDir || !e.IsDir || e.Name != "reports" {
		t.Fatalf("the created entry is %+v, want a directory named reports", e)
	}
	if names := names(mustList(t, c, root, "", ListOptions{})); !equalNames(names, []string{"reports"}) {
		t.Fatalf("the listing after Mkdir is %v", names)
	}

	if _, aerr := c.Mkdir(context.Background(), dir); !errors.Is(aerr, ErrExists) {
		t.Fatalf("re-creating = %v, want ErrExists", aerr)
	}

	// The creation table refuses names a Windows or SMB client could never
	// open, and a control prefix is refused before that.
	for _, name := range []string{"CON", "trailing.", "colon:name"} {
		p, perr := vfs.ParseVpath("Documents/" + name)
		if perr != nil {
			// The vpath parser already refuses some of these, which is the
			// same table one layer earlier.
			continue
		}
		r, rerr := c.Resolve(1, p, acl.Create)
		if rerr != nil {
			continue
		}
		if _, merr := c.Mkdir(context.Background(), r); merr == nil {
			t.Fatalf("Mkdir(%q) was allowed", name)
		}
	}

	// Without the bit, nothing reaches the disk.
	seedUser(t, st, 2, "bob")
	grantAt(t, c, st, 2, 10, "", "Documents", acl.Read)
	reader, rerr := c.Resolve(2, vpath(t, "Documents/denied"), acl.Read)
	if rerr != nil {
		t.Fatalf("resolving for the reader: %v", rerr)
	}
	if _, merr := c.Mkdir(context.Background(), reader); !errors.Is(merr, ErrDenied) {
		t.Fatalf("Mkdir without Create = %v, want ErrDenied", merr)
	}
	if _, serr := os.Stat(filepath.Join(host, "denied")); !os.IsNotExist(serr) {
		t.Fatal("the refused Mkdir created the directory anyway")
	}
}

func TestCreateFileWritesReplacesAndPreservesMode(t *testing.T) {
	c, _, host, _ := writable(t)
	r := under(t, c, "Documents/data.bin", acl.Write)

	e := mustCreate(t, c, r, "first")
	if e.Size != 5 || e.ETag == "" || e.Kind != vfs.KindFile {
		t.Fatalf("the created entry is %+v", e)
	}
	if got := readHost(t, host, "data.bin"); got != "first" {
		t.Fatalf("the file holds %q", got)
	}
	info, err := os.Stat(filepath.Join(host, "data.bin"))
	if err != nil {
		t.Fatalf("stating the created file: %v", err)
	}
	if info.Mode().Perm() != 0o644 {
		t.Fatalf("the created file's mode is %v, want 0644", info.Mode().Perm())
	}

	// A replace keeps the prior file's mode rather than the caller's, which
	// is what stops an upload from widening a file's access.
	if cerr := os.Chmod(filepath.Join(host, "data.bin"), 0o600); cerr != nil {
		t.Fatalf("narrowing the mode: %v", cerr)
	}
	if _, rerr := c.CreateFile(context.Background(), r,
		vfs.DurableOpts{Mode: 0o666}, nil, writeAll("second")); rerr != nil {
		t.Fatalf("replacing: %v", rerr)
	}
	if got := readHost(t, host, "data.bin"); got != "second" {
		t.Fatalf("the replaced file holds %q", got)
	}
	info, err = os.Stat(filepath.Join(host, "data.bin"))
	if err != nil {
		t.Fatalf("stating the replaced file: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("the replace changed the mode to %v, want the prior 0600", info.Mode().Perm())
	}
}

// TestAReplaceIsAtomicForAReaderHoldingTheOldDescriptor is the durable-write
// property asserted once, here, where it is consumed: the replace publishes
// by rename, so an open descriptor keeps reading the bytes it opened.
func TestAReplaceIsAtomicForAReaderHoldingTheOldDescriptor(t *testing.T) {
	c, _, host, _ := writable(t)
	writeFile(t, host, "live.txt", "before")
	r := under(t, c, "Documents/live.txt", acl.Write)

	held, err := os.Open(filepath.Join(host, "live.txt"))
	if err != nil {
		t.Fatalf("opening the file: %v", err)
	}
	defer func() {
		if cerr := held.Close(); cerr != nil {
			t.Errorf("closing the held descriptor: %v", cerr)
		}
	}()

	if _, cerr := c.CreateFile(context.Background(), r,
		vfs.DurableOpts{Mode: 0o644}, nil, writeAll("after!")); cerr != nil {
		t.Fatalf("replacing: %v", cerr)
	}
	buf := make([]byte, 6)
	if _, rerr := held.ReadAt(buf, 0); rerr != nil {
		t.Fatalf("reading the held descriptor: %v", rerr)
	}
	if string(buf) != "before" {
		t.Fatalf("the held descriptor sees %q, want the bytes it opened", buf)
	}
}

func TestAFailedWriteCallbackLeavesNothingBehind(t *testing.T) {
	c, _, host, root := writable(t)
	writeFile(t, host, "keep.txt", "original")
	r := under(t, c, "Documents/keep.txt", acl.Write)

	boom := errors.New("the caller gave up")
	_, err := c.CreateFile(context.Background(), r, vfs.DurableOpts{Mode: 0o644}, nil,
		func(*vfs.File) error { return boom })
	if !errors.Is(err, boom) {
		t.Fatalf("a failing callback = %v, want the callback's own error", err)
	}
	if got := readHost(t, host, "keep.txt"); got != "original" {
		t.Fatalf("the failed write left %q", got)
	}
	// The staging file is a control name, so a listing would not show it
	// anyway; the assertion that matters is that nothing was published.
	if got := names(mustList(t, c, root, "", ListOptions{})); !equalNames(got, []string{"keep.txt"}) {
		t.Fatalf("the listing after the failed write is %v", got)
	}
}

func TestRenameMovesWithinTheDirectory(t *testing.T) {
	c, _, host, root := writable(t)
	writeFile(t, host, "draft.txt", "text")
	r := under(t, c, "Documents/draft.txt", acl.Rename)

	e, err := c.Rename(context.Background(), r, "final.txt", nil)
	if err != nil {
		t.Fatalf("Rename: %v", err)
	}
	if e.Name != "final.txt" || e.Path.String() != "final.txt" {
		t.Fatalf("the renamed entry is %+v", e)
	}
	if got := names(mustList(t, c, root, "", ListOptions{})); !equalNames(got, []string{"final.txt"}) {
		t.Fatalf("the listing after the rename is %v", got)
	}

	// A taken destination is refused atomically rather than replaced.
	writeFile(t, host, "other.txt", "other")
	again := under(t, c, "Documents/final.txt", acl.Rename)
	if _, terr := c.Rename(context.Background(), again, "other.txt", nil); !errors.Is(terr, ErrExists) {
		t.Fatalf("renaming onto a taken name = %v, want ErrExists", terr)
	}
	if got := readHost(t, host, "other.txt"); got != "other" {
		t.Fatalf("the refused rename overwrote the destination: %q", got)
	}

	// A hostile new name is refused by the creation table, source intact.
	if _, herr := c.Rename(context.Background(), again, "bad.", nil); herr == nil {
		t.Fatal("a trailing-dot destination was allowed")
	}
	if got := readHost(t, host, "final.txt"); got != "text" {
		t.Fatalf("the refused rename disturbed the source: %q", got)
	}

	// A supplied validator refuses with the source's own current token.
	st, serr := again.Root().Stat(again.Path())
	if serr != nil {
		t.Fatalf("stating the source: %v", serr)
	}
	want, _ := FileETag(st)
	tok := Token("v1")
	_, perr := c.Rename(context.Background(), again, "renamed.txt", &tok)
	var pe *PreconditionError
	if !errors.As(perr, &pe) || pe.Current != want {
		t.Fatalf("a validated rename = %v, want the source's token %q", perr, want)
	}
}

func TestDeleteRemovesFilesAndTreesAndCreditsTheLedger(t *testing.T) {
	c, _, host, _ := writable(t)
	sink := attachSink(t, c)
	writeFile(t, host, "gone.txt", "0123456789")

	file := under(t, c, "Documents/gone.txt", acl.Delete)
	if err := c.Delete(context.Background(), file, false); err != nil {
		t.Fatalf("deleting the file: %v", err)
	}
	if _, serr := os.Stat(filepath.Join(host, "gone.txt")); !os.IsNotExist(serr) {
		t.Fatal("the deleted file is still there")
	}
	if len(sink.released) != 1 || sink.released[0] != 10 {
		t.Fatalf("the file delete credited %v, want one credit of 10", sink.released)
	}

	// A directory is credited its recursive size, read before anything is
	// unlinked, and removed bottom-up.
	if err := os.MkdirAll(filepath.Join(host, "tree/inner"), 0o755); err != nil {
		t.Fatalf("building the tree: %v", err)
	}
	writeFile(t, host, "tree/a.txt", "aaa")
	writeFile(t, host, "tree/inner/b.txt", "bbbb")

	dir := under(t, c, "Documents/tree", acl.Delete)
	if err := c.Delete(context.Background(), dir, false); err != nil {
		t.Fatalf("deleting the tree: %v", err)
	}
	if _, serr := os.Stat(filepath.Join(host, "tree")); !os.IsNotExist(serr) {
		t.Fatal("the deleted tree is still there")
	}
	if len(sink.released) != 2 || sink.released[1] != 7 {
		t.Fatalf("the tree delete credited %v, want a second credit of 7", sink.released)
	}
}

func TestDeleteWithoutTheBitTouchesNothing(t *testing.T) {
	c, st, host, _ := writable(t)
	writeFile(t, host, "safe.txt", "x")
	seedUser(t, st, 2, "bob")
	grantAt(t, c, st, 2, 10, "", "Documents", acl.Read)

	r, err := c.Resolve(2, vpath(t, "Documents/safe.txt"), acl.Read)
	if err != nil {
		t.Fatalf("resolving for the reader: %v", err)
	}
	if derr := c.Delete(context.Background(), r, false); !errors.Is(derr, ErrDenied) {
		t.Fatalf("deleting without the bit = %v, want ErrDenied", derr)
	}
	if got := readHost(t, host, "safe.txt"); got != "x" {
		t.Fatalf("the refused delete disturbed the file: %q", got)
	}
}

// TestADanglingChildDoesNotFailARecursiveDelete uses a dangling symlink,
// whose stat fails exactly as a vanished child's does.
func TestADanglingChildDoesNotFailARecursiveDelete(t *testing.T) {
	c, _, host, _ := writable(t)
	if err := os.Mkdir(filepath.Join(host, "mixed"), 0o755); err != nil {
		t.Fatalf("creating the directory: %v", err)
	}
	writeFile(t, host, "mixed/real.txt", "x")
	if err := os.Symlink("nowhere", filepath.Join(host, "mixed/ghost.txt")); err != nil {
		t.Fatalf("creating the dangling symlink: %v", err)
	}

	r := under(t, c, "Documents/mixed", acl.Delete)
	err := c.Delete(context.Background(), r, false)
	// The ghost is skipped by the walk, so the Rmdir backstop refuses to
	// report a directory as deleted while it still holds something.
	if err != nil && !errors.Is(err, ErrNotEmpty) {
		t.Fatalf("deleting a tree with a dangling child = %v", err)
	}
	if err == nil {
		if _, serr := os.Stat(filepath.Join(host, "mixed")); !os.IsNotExist(serr) {
			t.Fatal("the delete reported success with the directory still there")
		}
	}
}

func TestStatProjectsTheResolvedPath(t *testing.T) {
	c, st, host, _ := writable(t)
	writeFile(t, host, "one.txt", "hello")

	r := under(t, c, "Documents/one.txt", acl.Read)
	e, err := c.Stat(context.Background(), r)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if e.Name != "one.txt" || e.Size != 5 || e.Kind != vfs.KindFile || e.ETag == "" {
		t.Fatalf("the stat entry is %+v", e)
	}

	// A vanished target comes back as the skeleton, detected by the zero
	// kind rather than by an error.
	missing := under(t, c, "Documents/absent.txt", acl.Read)
	skeleton, serr := c.Stat(context.Background(), missing)
	if serr != nil {
		t.Fatalf("stating a missing path: %v", serr)
	}
	if skeleton.Kind != vfs.KindOther || skeleton.ETag != "" {
		t.Fatalf("the skeleton entry is %+v", skeleton)
	}

	// A drop-style subtree: the caller may take the bytes but may not
	// inspect the tree, so a resolution earned with Download carries no
	// Read bit and Stat refuses before the path is touched.
	seedUser(t, st, 2, "bob")
	grantAt(t, c, st, 2, 10, "", "Documents", acl.Read|acl.Download)
	denyReadAt(t, c, st, 2, 10, "one.txt", acl.Download)
	drop, derr := c.Resolve(2, vpath(t, "Documents/one.txt"), acl.Download)
	if derr != nil {
		t.Fatalf("resolving for the drop grant: %v", derr)
	}
	if _, serr := c.Stat(context.Background(), drop); !errors.Is(serr, ErrDenied) {
		t.Fatalf("Stat without Read = %v, want ErrDenied", serr)
	}
}

func TestPublishPartRefusesAnythingItDidNotMint(t *testing.T) {
	c, _, host, _ := writable(t)
	r := under(t, c, "Documents/final.bin", acl.Write)

	// A part that is not a control name cannot have come from the upload
	// engine, whatever it is called.
	ordinary := safe(t, "ordinary.tmp")
	if _, err := c.PublishPart(context.Background(), r, ordinary, 4); !errors.Is(err, ErrDenied) {
		t.Fatalf("publishing an ordinary name = %v, want ErrDenied", err)
	}

	// A part in another directory would make the rename two steps.
	if err := os.Mkdir(filepath.Join(host, "elsewhere"), 0o755); err != nil {
		t.Fatalf("creating the other directory: %v", err)
	}
	elsewhere, err := safe(t, "elsewhere").JoinControl(".scpart-abcd")
	if err != nil {
		t.Fatalf("building the part path: %v", err)
	}
	if _, perr := c.PublishPart(context.Background(), r, elsewhere, 4); !errors.Is(perr, ErrDenied) {
		t.Fatalf("publishing across directories = %v, want ErrDenied", perr)
	}
}

func TestPublishPartCreatesAndReplacesWithTheSignedCharge(t *testing.T) {
	c, _, host, _ := writable(t)
	sink := attachSink(t, c)

	part, err := vfs.RootPath().JoinControl(".scpart-first")
	if err != nil {
		t.Fatalf("building the part path: %v", err)
	}
	writeFile(t, host, ".scpart-first", "0123456789")

	dest := under(t, c, "Documents/upload.bin", acl.Write)
	e, perr := c.PublishPart(context.Background(), dest, part, 10)
	if perr != nil {
		t.Fatalf("PublishPart: %v", perr)
	}
	if e.Size != 10 || e.Name != "upload.bin" {
		t.Fatalf("the published entry is %+v", e)
	}
	if got := readHost(t, host, "upload.bin"); got != "0123456789" {
		t.Fatalf("the published file holds %q", got)
	}
	if _, serr := os.Stat(filepath.Join(host, ".scpart-first")); !os.IsNotExist(serr) {
		t.Fatal("the part file survived the publish")
	}
	if len(sink.reserved) != 1 || sink.reserved[0] != 10 {
		t.Fatalf("creating charged %v, want one booking of 10", sink.reserved)
	}

	// A replace keeps the prior mode and charges only the difference, which
	// here is negative because the replacement is smaller.
	if cerr := os.Chmod(filepath.Join(host, "upload.bin"), 0o600); cerr != nil {
		t.Fatalf("narrowing the mode: %v", cerr)
	}
	second, err := vfs.RootPath().JoinControl(".scpart-second")
	if err != nil {
		t.Fatalf("building the second part path: %v", err)
	}
	writeFile(t, host, ".scpart-second", "abcd")
	if _, perr := c.PublishPart(context.Background(), dest, second, 4); perr != nil {
		t.Fatalf("republishing: %v", perr)
	}
	if got := readHost(t, host, "upload.bin"); got != "abcd" {
		t.Fatalf("the replaced file holds %q", got)
	}
	info, serr := os.Stat(filepath.Join(host, "upload.bin"))
	if serr != nil {
		t.Fatalf("stating the replaced file: %v", serr)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("the replace changed the mode to %v, want the prior 0600", info.Mode().Perm())
	}
	if len(sink.released) != 1 || sink.released[0] != 6 {
		t.Fatalf("the shrinking replace credited %v, want one credit of 6", sink.released)
	}
}

func TestDeltaOfIsSignedAndRefusesGarbage(t *testing.T) {
	cases := []struct {
		now, before uint64
		want        int64
	}{
		{10, 0, 10},
		{4, 10, -6},
		{7, 7, 0},
		{1 << 63, 0, 0},
		{0, 1 << 63, 0},
	}
	for _, tc := range cases {
		if got := deltaOf(tc.now, tc.before); got != tc.want {
			t.Fatalf("deltaOf(%d, %d) = %d, want %d", tc.now, tc.before, got, tc.want)
		}
	}
}

func TestInt64MinusSaturatesRatherThanWrapping(t *testing.T) {
	cases := []struct {
		in   uint64
		want int64
	}{
		{0, 0},
		{10, -10},
		{1<<63 - 1, -(1<<63 - 1)},
		{1 << 63, -1 << 63},
		{^uint64(0), -1 << 63},
	}
	for _, tc := range cases {
		if got := int64Minus(tc.in); got != tc.want {
			t.Fatalf("int64Minus(%d) = %d, want %d", tc.in, got, tc.want)
		}
	}
}

func TestTheJournalRecordsWhatCommittedAndNothingElse(t *testing.T) {
	c, _, host, _ := writable(t)
	j := attachJournal(t, c)
	ctx := context.Background()

	writeFile(t, host, "src.txt", "x")
	if _, err := c.Mkdir(ctx, under(t, c, "Documents/made", acl.Create)); err != nil {
		t.Fatalf("Mkdir: %v", err)
	}
	mustCreate(t, c, under(t, c, "Documents/written.txt", acl.Write), "y")
	if _, err := c.Rename(ctx, under(t, c, "Documents/src.txt", acl.Rename), "moved.txt", nil); err != nil {
		t.Fatalf("Rename: %v", err)
	}
	if err := c.Delete(ctx, under(t, c, "Documents/moved.txt", acl.Delete), true); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	rows, err := j.Recent(ctx, 1, 50)
	if err != nil {
		t.Fatalf("reading the journal: %v", err)
	}
	ops := map[string]journal.Op{}
	for _, row := range rows {
		ops[row.Path.String()] = row.Op
	}
	if ops["made"] != journal.OpUpload {
		t.Fatalf("Mkdir recorded %v, want upload", ops["made"])
	}
	if ops["written.txt"] != journal.OpUpload {
		t.Fatalf("CreateFile recorded %v, want upload", ops["written.txt"])
	}
	if ops["moved.txt"] != journal.OpMove {
		t.Fatalf("Rename recorded %v, want move", ops["moved.txt"])
	}
	// A delete has nothing to surface in a recent-files list, so it writes
	// no row; the rename's row for the same path is what remains.
	if len(rows) != 3 {
		t.Fatalf("the journal holds %d rows, want three: %+v", len(rows), rows)
	}
}

func TestAFailedMutationWritesNoJournalRow(t *testing.T) {
	c, _, host, _ := writable(t)
	j := attachJournal(t, c)
	ctx := context.Background()

	writeFile(t, host, "taken.txt", "x")
	writeFile(t, host, "other.txt", "y")
	if _, err := c.Rename(ctx, under(t, c, "Documents/taken.txt", acl.Rename),
		"other.txt", nil); !errors.Is(err, ErrExists) {
		t.Fatalf("the rename was expected to be refused, got %v", err)
	}
	rows, err := j.Recent(ctx, 1, 50)
	if err != nil {
		t.Fatalf("reading the journal: %v", err)
	}
	if len(rows) != 0 {
		t.Fatalf("a refused rename wrote %d rows: %+v", len(rows), rows)
	}
}

// TestAnAccountPastTheJournalColumnIsSkipped covers the narrowing guard: a
// row that does not fit is dropped rather than truncated into some other
// account's history.
func TestAnAccountPastTheJournalColumnIsSkipped(t *testing.T) {
	c, st, host, _ := writable(t)
	j := attachJournal(t, c)
	ctx := context.Background()

	const big = int64(1) << 40
	seedUser(t, st, big, "titan")
	grantAt(t, c, st, big, 10, "", "Documents", allPerms)
	_ = host

	r, err := c.Resolve(UserID(big), vpath(t, "Documents/titan.txt"), acl.Write)
	if err != nil {
		t.Fatalf("resolving for the large account: %v", err)
	}
	mustCreate(t, c, r, "x")

	// The write itself committed; only the row was dropped. Nothing lands
	// in the truncated account either.
	truncated := uint32(big & 0xffffffff)
	rows, err := j.Recent(ctx, truncated, 50)
	if err != nil {
		t.Fatalf("reading the journal: %v", err)
	}
	if len(rows) != 0 {
		t.Fatalf("the truncated account holds %d rows: %+v", len(rows), rows)
	}
	if got := readHost(t, host, "titan.txt"); got != "x" {
		t.Fatalf("the write did not commit: %q", got)
	}
}

// A journal that cannot write does not fail the mutation. The row feeds the
// recent-files listing, so losing it costs a convenience surface; failing the
// write over it would lose the user's data to protect a listing.
//
// The journal is made to fail by closing the database under it rather than by
// substituting a stub, so what is exercised is the real Record path returning
// a real error.
func TestAFailingJournalDoesNotFailAWrite(t *testing.T) {
	c, _, host, _ := writable(t)

	f, err := dbfile.Open(context.Background(),
		journal.Spec(filepath.Join(t.TempDir(), "doomed.db")))
	if err != nil {
		t.Fatalf("opening the journal: %v", err)
	}
	c.journal = journal.New(f, clock.System())
	if cerr := f.Close(); cerr != nil {
		t.Fatalf("closing the journal: %v", cerr)
	}

	// The precondition for the test: this journal really does refuse.
	probe, perr := vfs.ParseSharePath("probe.txt")
	if perr != nil {
		t.Fatalf("ParseSharePath: %v", perr)
	}
	if rerr := c.journal.Record(context.Background(), journal.Event{
		Account: 1, Path: probe, Op: journal.OpUpload,
	}); rerr == nil {
		t.Fatal("the closed journal accepted a row, so this test proves nothing")
	}

	mustCreate(t, c, under(t, c, "Documents/despite.txt", acl.Write), "kept")
	if got := readHost(t, host, "despite.txt"); got != "kept" {
		t.Fatalf("a failing journal cost the write: the file holds %q", got)
	}
}

func TestANilJournalDoesNotFailAWrite(t *testing.T) {
	c, _, host, _ := writable(t)
	if c.journal != nil {
		t.Fatal("the fixture core already carries a journal")
	}
	mustCreate(t, c, under(t, c, "Documents/nojournal.txt", acl.Write), "x")
	if got := readHost(t, host, "nojournal.txt"); got != "x" {
		t.Fatalf("the write with no journal left %q", got)
	}
}

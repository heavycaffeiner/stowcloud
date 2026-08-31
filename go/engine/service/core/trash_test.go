//go:build linux

package core

import (
	"context"
	"encoding/base64"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/heavycaffeiner/stowcloud/go/engine/infra/vfs"
	"github.com/heavycaffeiner/stowcloud/go/engine/service/acl"
	"github.com/heavycaffeiner/stowcloud/go/engine/store/state"
)

// trashable is a share with the trash turned on, which is what makes Delete
// relocate instead of remove.
func trashable(t *testing.T) (c *Core, st *state.DB, host string, root Resolved) {
	t.Helper()
	c, st = newCore(t)
	seedUser(t, st, 1, "ada")

	d := def(t, 10, "documents")
	d.TrashEnabled = true
	if err := c.RegisterShare(context.Background(), d); err != nil {
		t.Fatalf("registering the trash-enabled share: %v", err)
	}
	grantAt(t, c, st, 1, 10, "", "Documents", allPerms)

	root, err := c.Resolve(1, vpath(t, "Documents"), acl.Read)
	if err != nil {
		t.Fatalf("resolving the share root: %v", err)
	}
	return c, st, d.Host, root
}

// trashNames is what is on disk under the control directory.
func trashNames(t *testing.T, hostDir string) []string {
	t.Helper()
	entries, err := os.ReadDir(filepath.Join(hostDir, trashDir))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		t.Fatalf("reading the trash directory: %v", err)
	}
	out := make([]string, 0, len(entries))
	for _, e := range entries {
		out = append(out, e.Name())
	}
	return out
}

func TestEncodingRoundTripsEveryOriginPath(t *testing.T) {
	for _, raw := range []string{
		"report.pdf",
		"docs/2024/report.pdf",
		"a-name-with-dashes.txt",
		"spaced out name.txt",
		"日本語のファイル.txt",
		"deep/nested/tree/leaf",
	} {
		p := safe(t, raw)
		got, ok := decodeOrigPath(encodeOrigPath(p))
		if !ok {
			t.Fatalf("decoding %q reported not-ok", raw)
		}
		if got.String() != raw {
			t.Fatalf("round-tripping %q gave %q", raw, got.String())
		}
	}
}

func TestDecodeReportsNotOkForAnythingItDidNotWrite(t *testing.T) {
	// A legacy entry carries a bare basename, which is exactly how it is
	// recognised.
	if _, ok := decodeOrigPath("report.pdf"); ok {
		t.Fatal("a bare basename decoded as an origin path")
	}
	if _, ok := decodeOrigPath("!!!not base64!!!"); ok {
		t.Fatal("invalid base64 decoded")
	}
	// Valid base64 whose bytes are not a path this server would accept.
	bad := base64.RawURLEncoding.EncodeToString([]byte("../escape"))
	if _, ok := decodeOrigPath(bad); ok {
		t.Fatal("base64 of a traversal decoded as an origin path")
	}
}

func TestSplitTrashNameCutsOnTheFirstDash(t *testing.T) {
	// The encoded half may hold dashes; the hex id never can, so the first
	// dash is always the right cut.
	id, rest, ok := splitTrashName("0011aabb-ZG9jcy0yMDI0")
	if !ok || id != "0011aabb" || rest != "ZG9jcy0yMDI0" {
		t.Fatalf("splitting gave %q, %q, %v", id, rest, ok)
	}
	id, rest, ok = splitTrashName("0011aabb-a-b-c")
	if !ok || id != "0011aabb" || rest != "a-b-c" {
		t.Fatalf("splitting a dashed suffix gave %q, %q, %v", id, rest, ok)
	}
	if _, _, ok := splitTrashName("nodashhere"); ok {
		t.Fatal("a name with no dash reported ok")
	}
	if _, _, ok := splitTrashName("-leading"); ok {
		t.Fatal("a name with an empty id reported ok")
	}
}

func TestHexLowerIsLowercaseAndDashFree(t *testing.T) {
	got := hexLower([]byte{0x00, 0x0f, 0xa5, 0xff, 0x10})
	if got != "000fa5ff10" {
		t.Fatalf("hexLower rendered %q", got)
	}
	id, err := newTrashID()
	if err != nil {
		t.Fatalf("minting an id: %v", err)
	}
	if len(id) != trashIDBytes*2 || strings.ContainsAny(id, "-ABCDEF") {
		t.Fatalf("a minted id is %q", id)
	}
}

func TestDeleteOnATrashEnabledShareRelocates(t *testing.T) {
	c, _, hostDir, root := trashable(t)
	sink := attachSink(t, c)
	if err := os.MkdirAll(filepath.Join(hostDir, "docs/2024"), 0o755); err != nil {
		t.Fatalf("building the tree: %v", err)
	}
	writeFile(t, hostDir, "docs/2024/report.pdf", "content")

	r := under(t, c, "Documents/docs/2024/report.pdf", acl.Delete)
	if err := c.Delete(context.Background(), r, false); err != nil {
		t.Fatalf("deleting into the trash: %v", err)
	}
	if _, serr := os.Stat(filepath.Join(hostDir, "docs/2024/report.pdf")); !os.IsNotExist(serr) {
		t.Fatal("the origin survived the trashing")
	}

	entries := trashNames(t, hostDir)
	if len(entries) != 1 {
		t.Fatalf("the trash holds %v, want one entry", entries)
	}
	_, rest, ok := splitTrashName(entries[0])
	if !ok {
		t.Fatalf("the entry name %q does not split", entries[0])
	}
	orig, ok := decodeOrigPath(rest)
	if !ok || orig.String() != "docs/2024/report.pdf" {
		t.Fatalf("the entry name decodes to %q", orig.String())
	}

	// The bytes are still on disk, only relocated, so nothing is credited
	// until the purge.
	if len(sink.released) != 0 || len(sink.reserved) != 0 {
		t.Fatalf("trashing moved the ledger: %v %v", sink.released, sink.reserved)
	}
	// The control directory never appears in a listing.
	if got := names(mustList(t, c, root, "", ListOptions{})); !equalNames(got, []string{"docs"}) {
		t.Fatalf("the root listing is %v, want the trash hidden", got)
	}
}

func TestTwoDeletesOfOnePathCoexist(t *testing.T) {
	c, _, hostDir, _ := trashable(t)
	ctx := context.Background()
	for range 2 {
		writeFile(t, hostDir, "same.txt", "x")
		r := under(t, c, "Documents/same.txt", acl.Delete)
		if err := c.Delete(ctx, r, false); err != nil {
			t.Fatalf("deleting: %v", err)
		}
	}
	if got := trashNames(t, hostDir); len(got) != 2 {
		t.Fatalf("the trash holds %v, want two entries", got)
	}
}

func TestPermanentAndDisabledDeletesRemoveForGood(t *testing.T) {
	c, _, hostDir, _ := trashable(t)
	sink := attachSink(t, c)
	writeFile(t, hostDir, "bypass.txt", "0123456789")

	r := under(t, c, "Documents/bypass.txt", acl.Delete)
	if err := c.Delete(context.Background(), r, true); err != nil {
		t.Fatalf("deleting permanently: %v", err)
	}
	if got := trashNames(t, hostDir); len(got) != 0 {
		t.Fatalf("a permanent delete left %v in the trash", got)
	}
	if len(sink.released) != 1 || sink.released[0] != 10 {
		t.Fatalf("a permanent delete credited %v, want one credit of 10", sink.released)
	}

	// A share without trash deletes permanently either way.
	plain, _, plainHost, _ := writable(t)
	plainSink := attachSink(t, plain)
	writeFile(t, plainHost, "gone.txt", "abcde")
	pr := under(t, plain, "Documents/gone.txt", acl.Delete)
	if err := plain.Delete(context.Background(), pr, false); err != nil {
		t.Fatalf("deleting on a trash-less share: %v", err)
	}
	if got := trashNames(t, plainHost); len(got) != 0 {
		t.Fatalf("a trash-less share created a trash: %v", got)
	}
	if len(plainSink.released) != 1 || plainSink.released[0] != 5 {
		t.Fatalf("the trash-less delete credited %v", plainSink.released)
	}
}

func TestTrashListReportsWhatWasDeleted(t *testing.T) {
	c, _, hostDir, root := trashable(t)
	ctx := context.Background()

	// A share with nothing deleted has no control directory at all, and
	// that is not a fault.
	empty, err := c.TrashList(ctx, root)
	if err != nil || len(empty) != 0 {
		t.Fatalf("listing an untouched trash = %v, %v", empty, err)
	}

	if merr := os.MkdirAll(filepath.Join(hostDir, "docs"), 0o755); merr != nil {
		t.Fatalf("building the tree: %v", merr)
	}
	writeFile(t, hostDir, "docs/note.txt", "hello")
	// An old mtime, so the listing's timestamp cannot be inherited from it.
	old := time.Unix(1_000_000_000, 0)
	if cerr := os.Chtimes(filepath.Join(hostDir, "docs/note.txt"), old, old); cerr != nil {
		t.Fatalf("stamping the file: %v", cerr)
	}
	if derr := c.Delete(ctx, under(t, c, "Documents/docs/note.txt", acl.Delete), false); derr != nil {
		t.Fatalf("trashing: %v", derr)
	}

	rows, err := c.TrashList(ctx, root)
	if err != nil {
		t.Fatalf("TrashList: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("the trash lists %d entries", len(rows))
	}
	row := rows[0]
	if row.Name != "note.txt" || row.OrigPath != "docs/note.txt" {
		t.Fatalf("the row is %+v", row)
	}
	if row.IsDir || row.Size != 5 || row.ID == "" {
		t.Fatalf("the row carries %+v", row)
	}
	// The move into the trash is an inode change, which is what records
	// when the delete happened; the mtime a rename does not touch.
	if row.DeletedAtNs <= old.UnixNano() {
		t.Fatalf("the row reports deletion at %d, want a time after the file's own mtime", row.DeletedAtNs)
	}

	// A legacy entry carries a bare basename, and a non-conforming name is
	// not one of ours at all.
	legacy := filepath.Join(hostDir, trashDir, "aabbccdd-legacy.txt")
	if werr := os.WriteFile(legacy, []byte("old"), 0o644); werr != nil {
		t.Fatalf("planting the legacy entry: %v", werr)
	}
	junk := filepath.Join(hostDir, trashDir, "nodashhere")
	if werr := os.WriteFile(junk, []byte("junk"), 0o644); werr != nil {
		t.Fatalf("planting the junk entry: %v", werr)
	}
	rows, err = c.TrashList(ctx, root)
	if err != nil {
		t.Fatalf("TrashList after planting: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("the trash lists %d entries, want the junk name skipped: %+v", len(rows), rows)
	}
	var found bool
	for _, r := range rows {
		if r.ID == "aabbccdd" {
			found = true
			if r.Name != "legacy.txt" || r.OrigPath != "" {
				t.Fatalf("the legacy row is %+v, want the raw suffix and no origin", r)
			}
		}
	}
	if !found {
		t.Fatal("the legacy entry did not list")
	}

	// Read is what the listing demands. The root of a share is only
	// resolvable by somebody who holds Read there, so the refusal is
	// asserted against a resolution carrying the narrower set directly.
	stranger := Resolved{
		user: root.user, share: root.share, root: root.root,
		path: root.path, perms: acl.Download,
	}
	if _, lerr := c.TrashList(ctx, stranger); !errors.Is(lerr, ErrDenied) {
		t.Fatalf("listing without Read = %v, want ErrDenied", lerr)
	}
}

func TestTrashRestorePutsAnEntryBack(t *testing.T) {
	c, _, hostDir, root := trashable(t)
	ctx := context.Background()
	if err := os.MkdirAll(filepath.Join(hostDir, "docs/2024"), 0o755); err != nil {
		t.Fatalf("building the tree: %v", err)
	}
	writeFile(t, hostDir, "docs/2024/report.pdf", "content")
	if err := c.Delete(ctx, under(t, c, "Documents/docs/2024/report.pdf", acl.Delete), false); err != nil {
		t.Fatalf("trashing: %v", err)
	}
	// The origin's ancestors go too, so the restore has to recreate them.
	if err := os.RemoveAll(filepath.Join(hostDir, "docs")); err != nil {
		t.Fatalf("removing the ancestors: %v", err)
	}

	rows, err := c.TrashList(ctx, root)
	if err != nil || len(rows) != 1 {
		t.Fatalf("TrashList = %v, %v", rows, err)
	}
	dest, err := c.TrashRestore(ctx, root, rows[0].ID)
	if err != nil {
		t.Fatalf("TrashRestore: %v", err)
	}
	if dest.String() != "docs/2024/report.pdf" {
		t.Fatalf("the restore landed at %q", dest.String())
	}
	if got := readHost(t, hostDir, "docs/2024/report.pdf"); got != "content" {
		t.Fatalf("the restored file holds %q", got)
	}
	if got := trashNames(t, hostDir); len(got) != 0 {
		t.Fatalf("the trash still holds %v", got)
	}
}

func TestTrashRestoreRefusesToOverwriteAndToInvent(t *testing.T) {
	c, st, hostDir, root := trashable(t)
	ctx := context.Background()
	writeFile(t, hostDir, "note.txt", "old")
	if err := c.Delete(ctx, under(t, c, "Documents/note.txt", acl.Delete), false); err != nil {
		t.Fatalf("trashing: %v", err)
	}
	// Something newer is at the origin now.
	writeFile(t, hostDir, "note.txt", "new")

	rows, err := c.TrashList(ctx, root)
	if err != nil || len(rows) != 1 {
		t.Fatalf("TrashList = %v, %v", rows, err)
	}
	if _, rerr := c.TrashRestore(ctx, root, rows[0].ID); !errors.Is(rerr, ErrConflict) {
		t.Fatalf("restoring onto live data = %v, want ErrConflict", rerr)
	}
	if got := readHost(t, hostDir, "note.txt"); got != "new" {
		t.Fatalf("the refused restore overwrote the origin: %q", got)
	}
	if got := trashNames(t, hostDir); len(got) != 1 {
		t.Fatalf("the refused restore consumed the entry: %v", got)
	}

	if _, rerr := c.TrashRestore(ctx, root, "deadbeefdeadbeef"); !errors.Is(rerr, ErrNotFound) {
		t.Fatalf("restoring an unknown id = %v, want ErrNotFound", rerr)
	}

	// Create is the bit a restore demands, since it brings an entry into
	// the live tree.
	seedUser(t, st, 2, "bob")
	grantAt(t, c, st, 2, 10, "", "Documents", acl.Read|acl.Delete)
	reader, rerr := c.Resolve(2, vpath(t, "Documents"), acl.Read)
	if rerr != nil {
		t.Fatalf("resolving for the reader: %v", rerr)
	}
	if _, err := c.TrashRestore(ctx, reader, rows[0].ID); !errors.Is(err, ErrDenied) {
		t.Fatalf("restoring without Create = %v, want ErrDenied", err)
	}
}

func TestALegacyEntryRestoresToTheShareRoot(t *testing.T) {
	c, _, hostDir, root := trashable(t)
	ctx := context.Background()
	// Force the trash directory into existence through the ordinary path.
	writeFile(t, hostDir, "seed.txt", "x")
	if err := c.Delete(ctx, under(t, c, "Documents/seed.txt", acl.Delete), false); err != nil {
		t.Fatalf("seeding the trash: %v", err)
	}
	legacy := filepath.Join(hostDir, trashDir, "aabbccdd-legacy.txt")
	if err := os.WriteFile(legacy, []byte("old"), 0o644); err != nil {
		t.Fatalf("planting the legacy entry: %v", err)
	}

	dest, err := c.TrashRestore(ctx, root, "aabbccdd")
	if err != nil {
		t.Fatalf("restoring the legacy entry: %v", err)
	}
	if dest.String() != "legacy.txt" {
		t.Fatalf("the legacy restore landed at %q, want the share root", dest.String())
	}
	if got := readHost(t, hostDir, "legacy.txt"); got != "old" {
		t.Fatalf("the restored legacy file holds %q", got)
	}
}

func TestTrashPurgeRemovesAndCredits(t *testing.T) {
	c, _, hostDir, root := trashable(t)
	ctx := context.Background()
	sink := attachSink(t, c)

	writeFile(t, hostDir, "one.txt", "0123456789")
	if err := os.MkdirAll(filepath.Join(hostDir, "tree/inner"), 0o755); err != nil {
		t.Fatalf("building the tree: %v", err)
	}
	writeFile(t, hostDir, "tree/a.txt", "aaa")
	writeFile(t, hostDir, "tree/inner/b.txt", "bbbb")

	if err := c.Delete(ctx, under(t, c, "Documents/one.txt", acl.Delete), false); err != nil {
		t.Fatalf("trashing the file: %v", err)
	}
	if err := c.Delete(ctx, under(t, c, "Documents/tree", acl.Delete), false); err != nil {
		t.Fatalf("trashing the tree: %v", err)
	}

	rows, err := c.TrashList(ctx, root)
	if err != nil || len(rows) != 2 {
		t.Fatalf("TrashList = %v, %v", rows, err)
	}
	var fileID string
	for _, r := range rows {
		if !r.IsDir {
			fileID = r.ID
		}
	}

	// One id purges only its own entry.
	if perr := c.TrashPurge(ctx, root, &fileID); perr != nil {
		t.Fatalf("purging one entry: %v", perr)
	}
	if got := trashNames(t, hostDir); len(got) != 1 {
		t.Fatalf("purging one id left %v, want the directory entry", got)
	}
	if len(sink.released) != 1 || sink.released[0] != 10 {
		t.Fatalf("the file purge credited %v, want one credit of 10", sink.released)
	}

	// Nil purges the rest. The rollup refuses a path under a control
	// prefix, so the credit may be zero here; the delete is what must
	// happen, and it does.
	if perr := c.TrashPurge(ctx, root, nil); perr != nil {
		t.Fatalf("purging everything: %v", perr)
	}
	if got := trashNames(t, hostDir); len(got) != 0 {
		t.Fatalf("the full purge left %v", got)
	}
}

func TestTrashPurgeOnAnUntouchedShareSucceeds(t *testing.T) {
	c, _, _, root := trashable(t)
	if err := c.TrashPurge(context.Background(), root, nil); err != nil {
		t.Fatalf("purging a share with no trash directory: %v", err)
	}
}

func TestRestoreAndPurgeRefuseADisabledShare(t *testing.T) {
	c, _, hostDir, root := trashable(t)
	ctx := context.Background()
	writeFile(t, hostDir, "note.txt", "x")
	if err := c.Delete(ctx, under(t, c, "Documents/note.txt", acl.Delete), false); err != nil {
		t.Fatalf("trashing: %v", err)
	}
	rows, err := c.TrashList(ctx, root)
	if err != nil || len(rows) != 1 {
		t.Fatalf("TrashList = %v, %v", rows, err)
	}

	// The admin turns the trash off with content still in it.
	d, ok := c.Share(10)
	if !ok {
		t.Fatal("the share is not registered")
	}
	d.TrashEnabled = false
	if rerr := c.RegisterShare(ctx, d); rerr != nil {
		t.Fatalf("re-registering with the trash off: %v", rerr)
	}
	root = under(t, c, "Documents", acl.Read)

	if _, rerr := c.TrashRestore(ctx, root, rows[0].ID); !errors.Is(rerr, ErrTrashDisabled) {
		t.Fatalf("restoring on a disabled share = %v, want ErrTrashDisabled", rerr)
	}
	if perr := c.TrashPurge(ctx, root, nil); !errors.Is(perr, ErrTrashDisabled) {
		t.Fatalf("purging on a disabled share = %v, want ErrTrashDisabled", perr)
	}
	// The listing keeps answering, so a screen can show what would be lost
	// by purging out of band.
	if got, lerr := c.TrashList(ctx, root); lerr != nil || len(got) != 1 {
		t.Fatalf("listing a disabled share's trash = %v, %v", got, lerr)
	}
}

func TestCtimeOrMtimeFallsBackToTheMtime(t *testing.T) {
	ctime := int64(42)
	if got := ctimeOrMtime(vfs.Stat{MtimeNs: 7, CtimeNs: &ctime}); got != 42 {
		t.Fatalf("with a ctime present the answer is %d, want the ctime", got)
	}
	if got := ctimeOrMtime(vfs.Stat{MtimeNs: 7}); got != 7 {
		t.Fatalf("with no ctime the answer is %d, want the mtime", got)
	}
}

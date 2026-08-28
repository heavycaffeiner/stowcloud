//go:build linux

package preview

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"testing"

	"github.com/heavycaffeiner/stowcloud/go/engine/infra/vfs"
	"github.com/heavycaffeiner/stowcloud/go/engine/service/acl"
	"github.com/heavycaffeiner/stowcloud/go/engine/service/core"
	"github.com/heavycaffeiner/stowcloud/go/engine/store/cache"
	"github.com/heavycaffeiner/stowcloud/go/engine/store/dbfile"
	"github.com/heavycaffeiner/stowcloud/go/engine/store/state"
)

const (
	testUser   = core.UserID(1)
	shareLabel = "files"
)

// newService builds a service over a real core, a real cache directory and a
// real worker pool. Real ones rather than fakes: the rule under test is that a
// cache hit is not a permission bypass, and a fake cache would not prove it.
func newService(t *testing.T) *fixture {
	t.Helper()
	ctx := context.Background()
	dir := t.TempDir()

	stf, err := dbfile.Open(ctx, state.Spec(filepath.Join(dir, "state.db")))
	if err != nil {
		t.Fatalf("opening the state database: %v", err)
	}
	t.Cleanup(func() {
		if cerr := stf.Close(); cerr != nil {
			t.Errorf("closing the state database: %v", cerr)
		}
	})
	st := state.New(stf)

	cf, err := dbfile.Open(ctx, cache.Spec(filepath.Join(dir, "cache.db")))
	if err != nil {
		t.Fatalf("opening the cache database: %v", err)
	}
	t.Cleanup(func() {
		if cerr := cf.Close(); cerr != nil {
			t.Errorf("closing the cache database: %v", cerr)
		}
	})
	ca, err := cache.New(ctx, cf, st)
	if err != nil {
		t.Fatalf("wrapping the cache: %v", err)
	}

	evaluator := acl.NewEvaluator()
	c, err := core.New(ctx, core.Options{State: st, Cache: ca, ACL: evaluator})
	if err != nil {
		t.Fatalf("building the core: %v", err)
	}

	if werr := st.Write(ctx, func(tx *sql.Tx) error {
		_, ierr := tx.ExecContext(ctx,
			`INSERT INTO user(id, name, pw_hash, created_ns) VALUES (?, ?, '', 0)`,
			int64(testUser), "tester")
		return ierr
	}); werr != nil {
		t.Fatalf("seeding the user: %v", werr)
	}

	shareRoot := t.TempDir()
	share, serr := c.CreateShare(ctx, core.ShareSpec{Name: shareLabel, Host: shareRoot})
	if serr != nil {
		t.Skipf("this host's temp directory is on a filesystem this build refuses: %v", serr)
	}

	thumbs, err := NewCache(filepath.Join(dir, "thumbs"))
	if err != nil {
		t.Fatalf("NewCache: %v", err)
	}
	pool := newPool(t, 1, "")

	return &fixture{
		svc:   NewService(ServiceOptions{Core: c, Pool: pool, Cache: thumbs}),
		core:  c,
		state: st,
		share: share.ID,
		root:  shareRoot,
	}
}

// fixture is one service over one share, with everything a test needs to
// change the tree or the permissions underneath it.
type fixture struct {
	svc   *Service
	core  *core.Core
	state *state.DB
	share core.ShareID
	root  string
}

// grant persists one grant over the whole share and reloads the evaluator,
// which is the two-step discipline every grant write follows.
func (f *fixture) grant(t *testing.T, allow acl.Perms) {
	t.Helper()
	ctx := t.Context()
	holder := int64(testUser)
	if _, err := f.state.PersistGrant(ctx, state.GrantRow{
		User:    &holder,
		Share:   int64(f.share),
		Allow:   uint16(allow),
		Inherit: true,
		Label:   shareLabel,
	}, 0); err != nil {
		t.Fatalf("persisting the grant: %v", err)
	}
	if err := f.core.ReloadGrants(ctx); err != nil {
		t.Fatalf("reloading grants: %v", err)
	}
}

// writeImage puts a real image in the share and returns its share-relative
// name.
func (f *fixture) writeImage(t *testing.T, name string, w, h int) string {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := range h {
		for x := range w {
			img.Set(x, y, color.RGBA{R: uint8(x), G: uint8(y), B: 0x20, A: 0xff})
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatalf("encoding: %v", err)
	}
	if err := os.WriteFile(filepath.Join(f.root, name), buf.Bytes(), 0o600); err != nil {
		t.Fatalf("writing the image: %v", err)
	}
	return name
}

// resolve asks for a capability the way a request does: with the permission
// the caller intends to exercise.
func (f *fixture) resolve(t *testing.T, name string, need acl.Perms) (core.Resolved, error) {
	t.Helper()
	p, err := vfs.ParseVpath(shareLabel + "/" + name)
	if err != nil {
		t.Fatalf("parsing the vpath: %v", err)
	}
	return f.core.Resolve(testUser, p, need)
}

// mustResolve is resolve where the resolution itself is not what is under
// test.
func (f *fixture) mustResolve(t *testing.T, name string, need acl.Perms) core.Resolved {
	t.Helper()
	r, err := f.resolve(t, name, need)
	if err != nil {
		t.Fatalf("resolving %q: %v", name, err)
	}
	return r
}

// readOnlyUser seeds a second account granted Read over the share and nothing
// else, so a capability without Download can be obtained honestly rather than
// by editing one.
func (f *fixture) readOnlyUser(t *testing.T) core.UserID {
	t.Helper()
	ctx := t.Context()
	const id = core.UserID(2)

	if err := f.state.Write(ctx, func(tx *sql.Tx) error {
		_, werr := tx.ExecContext(ctx,
			`INSERT INTO user(id, name, pw_hash, created_ns) VALUES (?, ?, '', 0)`,
			int64(id), "reader")
		return werr
	}); err != nil {
		t.Fatalf("seeding the read-only user: %v", err)
	}

	holder := int64(id)
	if _, err := f.state.PersistGrant(ctx, state.GrantRow{
		User:    &holder,
		Share:   int64(f.share),
		Allow:   uint16(acl.Read),
		Inherit: true,
		Label:   shareLabel,
	}, 0); err != nil {
		t.Fatalf("granting the read-only user: %v", err)
	}
	if err := f.core.ReloadGrants(ctx); err != nil {
		t.Fatalf("reloading grants: %v", err)
	}
	return id
}

// resolveAs is resolve for an account other than the fixture's own.
func (f *fixture) resolveAs(
	t *testing.T, user core.UserID, name string, need acl.Perms,
) (core.Resolved, error) {
	t.Helper()
	p, err := vfs.ParseVpath(shareLabel + "/" + name)
	if err != nil {
		t.Fatalf("parsing the vpath: %v", err)
	}
	return f.core.Resolve(user, p, need)
}

// A thumbnail is a derivative of the bytes, so seeing one is seeing the file:
// the permission is the same a download needs.
func TestAThumbnailNeedsDownloadNotJustRead(t *testing.T) {
	f := newService(t)
	f.grant(t, acl.Read)
	name := f.writeImage(t, "photo.png", 200, 100)

	if _, err := f.svc.Get(t.Context(), f.mustResolve(t, name, acl.Read), PresetSmall); err == nil {
		t.Fatal("a caller with Read alone got a thumbnail")
	}
}

// The permission check runs before any cache lookup, so a planted entry is not
// a way in.
func TestACacheHitIsNotAPermissionBypass(t *testing.T) {
	f := newService(t)
	f.grant(t, acl.Read|acl.Download)
	name := f.writeImage(t, "photo.png", 200, 100)

	// Generate once, so the entry really is in the cache.
	thumb, err := f.svc.Get(t.Context(), f.mustResolve(t, name, acl.Read), PresetSmall)
	if err != nil {
		t.Fatalf("the first Get: %v", err)
	}
	if cerr := thumb.Close(); cerr != nil {
		t.Errorf("closing the thumbnail: %v", cerr)
	}

	// A second account that may read the share and may not download from it.
	// Its capability carries Read alone, so the check the service runs before
	// touching the cache is the only thing standing between it and an entry
	// that is already there.
	reader := f.readOnlyUser(t)
	r, err := f.resolveAs(t, reader, name, acl.Read)
	if err != nil {
		t.Fatalf("resolving as the read-only account: %v", err)
	}
	if r.Has(acl.Download) {
		t.Fatal("the read-only account resolved with Download")
	}
	if _, err := f.svc.Get(t.Context(), r, PresetSmall); err == nil {
		t.Error("a planted cache entry was served without Download")
	}
}

// End to end: a real image through service, pool and worker produces a
// thumbnail, and the second request is a cache hit.
func TestGetProducesAThumbnailAndThenHitsTheCache(t *testing.T) {
	f := newService(t)
	f.grant(t, acl.Read|acl.Download)
	name := f.writeImage(t, "photo.png", 400, 200)

	first, err := f.svc.Get(t.Context(), f.mustResolve(t, name, acl.Read), PresetSmall)
	if err != nil {
		t.Fatalf("the first Get: %v", err)
	}
	body, err := os.ReadFile(first.File.Name()) //nolint:gosec // G304: the cache path this service just wrote.
	if cerr := first.Close(); cerr != nil {
		t.Errorf("closing: %v", cerr)
	}
	if err != nil {
		t.Fatalf("reading the thumbnail: %v", err)
	}
	if Sniff(body) != FormatPNG {
		t.Errorf("the thumbnail is not a PNG: %d bytes", len(body))
	}

	// The second request is answered from the cache. Closing the pool proves
	// it: a generation would need a worker and there is none.
	if perr := f.svc.pool.Close(); perr != nil {
		t.Fatalf("closing the pool: %v", perr)
	}
	second, err := f.svc.Get(t.Context(), f.mustResolve(t, name, acl.Read), PresetSmall)
	if err != nil {
		t.Fatalf("the second Get did not come from the cache: %v", err)
	}
	if cerr := second.Close(); cerr != nil {
		t.Errorf("closing: %v", cerr)
	}
}

// An invalid preset refuses rather than falling back to a default, which would
// hide the caller's bug.
func TestAnInvalidPresetRefuses(t *testing.T) {
	f := newService(t)
	f.grant(t, acl.Read|acl.Download)
	name := f.writeImage(t, "photo.png", 40, 40)

	if _, err := f.svc.Get(t.Context(), f.mustResolve(t, name, acl.Read), Preset(99)); !errors.Is(err, ErrUnsupported) {
		t.Errorf("got %v, want ErrUnsupported", err)
	}
}

// A directory has no thumbnail, and neither does a path that is not there.
func TestADirectoryAndAMissingPathRefuse(t *testing.T) {
	f := newService(t)
	f.grant(t, acl.Read|acl.Download)
	if err := os.Mkdir(filepath.Join(f.root, "sub"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	if _, err := f.svc.Get(t.Context(), f.mustResolve(t, "sub", acl.Read), PresetSmall); !errors.Is(err, ErrUnsupported) {
		t.Errorf("a directory gave %v, want ErrUnsupported", err)
	}
}

// GetSized clamps into the supported range and differs in its key by either
// dimension.
func TestGetSizedClampsAndKeysOnBothDimensions(t *testing.T) {
	f := newService(t)
	f.grant(t, acl.Read|acl.Download)
	name := f.writeImage(t, "photo.png", 400, 200)

	sized, err := f.svc.GetSized(t.Context(), f.mustResolve(t, name, acl.Read), 100, 100)
	if err != nil {
		t.Fatalf("GetSized: %v", err)
	}
	sizedPath := sized.File.Name()
	if cerr := sized.Close(); cerr != nil {
		t.Errorf("closing: %v", cerr)
	}

	// A different size is a different entry, not the same one reused.
	other, err := f.svc.GetSized(t.Context(), f.mustResolve(t, name, acl.Read), 100, 101)
	if err != nil {
		t.Fatalf("GetSized at another size: %v", err)
	}
	otherPath := other.File.Name()
	if cerr := other.Close(); cerr != nil {
		t.Errorf("closing: %v", cerr)
	}
	if sizedPath == otherPath {
		t.Error("two different sizes share one cache entry")
	}

	// And a preset request is a third entry.
	preset, err := f.svc.Get(t.Context(), f.mustResolve(t, name, acl.Read), PresetSmall)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	presetPath := preset.File.Name()
	if cerr := preset.Close(); cerr != nil {
		t.Errorf("closing: %v", cerr)
	}
	if presetPath == sizedPath {
		t.Error("a sized preview collided with a preset one")
	}
}

// The bounds are clamped rather than refused, so a caller asking for zero or
// for something enormous gets the nearest supported size.
func TestSizedDimensionsAreClamped(t *testing.T) {
	for _, c := range []struct{ in, want int }{
		{0, MinSizedDimension},
		{-5, MinSizedDimension},
		{1, MinSizedDimension},
		{512, 512},
		{MaxSizedDimension, MaxSizedDimension},
		{MaxSizedDimension * 4, MaxSizedDimension},
	} {
		if got := clampDimension(c.in); got != c.want {
			t.Errorf("clampDimension(%d) = %d, want %d", c.in, got, c.want)
		}
	}
}

// The smallest preset whose box covers the request is the one the worker
// decodes against, so the result is scaled down rather than up.
func TestPresetForCoversTheRequestedBox(t *testing.T) {
	for _, c := range []struct {
		w, h int
		want Preset
	}{
		{16, 16, PresetSmall},
		{256, 256, PresetSmall},
		{257, 100, PresetMedium},
		{512, 512, PresetMedium},
		{800, 100, PresetLarge},
		{4096, 4096, PresetLarge},
	} {
		if got := presetFor(c.w, c.h); got != c.want {
			t.Errorf("presetFor(%d, %d) = %v, want %v", c.w, c.h, got, c.want)
		}
	}
}

// GetSized applies the same permission rule as Get.
func TestGetSizedNeedsDownloadToo(t *testing.T) {
	f := newService(t)
	f.grant(t, acl.Read)
	name := f.writeImage(t, "photo.png", 200, 100)

	if _, err := f.svc.GetSized(t.Context(), f.mustResolve(t, name, acl.Read), 64, 64); err == nil {
		t.Error("a caller with Read alone got a sized preview")
	}
}

// A source that fails to decode is remembered, so a corrupt file in a folder
// does not cost a worker on every listing.
func TestAFailedDecodeIsRememberedAsANegative(t *testing.T) {
	f := newService(t)
	f.grant(t, acl.Read|acl.Download)

	name := "broken.png"
	if err := os.WriteFile(filepath.Join(f.root, name),
		[]byte("\x89PNG\r\n\x1a\nnot really a png"), 0o600); err != nil {
		t.Fatalf("writing the broken file: %v", err)
	}

	if _, err := f.svc.Get(t.Context(), f.mustResolve(t, name, acl.Read), PresetSmall); err == nil {
		t.Fatal("a broken file produced a thumbnail")
	}
	if f.svc.neg.Len() == 0 {
		t.Error("the failure was not remembered")
	}

	// The second attempt is answered from the negative cache. Closing the pool
	// proves no worker was involved.
	if perr := f.svc.pool.Close(); perr != nil {
		t.Fatalf("closing the pool: %v", perr)
	}
	_, err := f.svc.Get(t.Context(), f.mustResolve(t, name, acl.Read), PresetSmall)
	if !errors.Is(err, ErrDecode) {
		t.Errorf("the remembered failure gave %v, want ErrDecode", err)
	}

	// The sweep is what keeps the map bounded.
	if f.svc.SweepNegatives() != 0 {
		t.Error("a fresh negative was swept")
	}
}

// The error mapping is what decides whether a failure is worth remembering: a
// busy or closed pool says nothing about the file.
func TestOnlyFileFactsAreRemembered(t *testing.T) {
	for err, want := range map[error]Negative{
		ErrTooLarge:       NegativeTooLarge,
		ErrUnsupported:    NegativeUnsupported,
		ErrNotImplemented: NegativeNotImplemented,
		ErrDecode:         NegativeDecodeFailed,
		ErrWorkerDied:     NegativeWorkerDied,
		ErrWorkerBusy:     NegativeNone,
		ErrPoolClosed:     NegativeNone,
	} {
		if got := reasonFor(err); got != want {
			t.Errorf("reasonFor(%v) = %v, want %v", err, got, want)
		}
	}
	// And every remembered reason maps back to an error a caller can act on.
	for _, n := range []Negative{
		NegativeTooLarge, NegativeUnsupported, NegativeNotImplemented,
		NegativeDecodeFailed, NegativeWorkerDied,
	} {
		if negativeError(n) == nil {
			t.Errorf("%v maps back to no error", n)
		}
	}
	if negativeError(NegativeNone) != nil {
		t.Error("the absence of a failure maps to an error")
	}
}

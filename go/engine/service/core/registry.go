//go:build linux

package core

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"

	"github.com/heavycaffeiner/stowcloud/go/engine/infra/vfs"
	"github.com/heavycaffeiner/stowcloud/go/engine/kit/num"
	"github.com/heavycaffeiner/stowcloud/go/engine/kit/secret"
	"github.com/heavycaffeiner/stowcloud/go/engine/service/acl"
)

// Share is one configured share as the admin-facing API returns it.
type Share struct {
	ID   ShareID
	Name string

	// Host is the on-disk path. Trusted server-side configuration; it must
	// never reach a client response. Meaningful only for BackendLocal; every
	// other backend leaves it empty.
	Host string

	// Policy holds the symlink, mode and ownership decisions.
	Policy vfs.SharePolicy

	// TrashEnabled is the admin-visible toggle, reported in the listing so
	// a delete confirmation can say whether the delete is undoable.
	TrashEnabled bool

	// SharedExternally flags a share that another service also reads, which the
	// client displays as a badge.
	SharedExternally bool

	// BrokenReason is a token explaining why this share is currently unservable,
	// empty when it is servable. It draws on the health surface's vocabulary, so
	// a screen and a probe asking the same question receive the same word.
	BrokenReason string

	// Backend names which package opens this share's storage: BackendLocal,
	// BackendS3 or BackendVeracrypt. Empty reads as BackendLocal, so a row
	// written before backends existed keeps meaning what it always meant.
	Backend string

	// Config is the backend's own JSON, secret-free, persisted verbatim.
	// core never parses it; only the backend package that owns Backend
	// does, through its own ParseConfig.
	Config []byte

	// Secret is the one credential a non-local backend needs to reach its
	// storage. It is sealed at rest by the durable layer and travels through
	// this type only long enough to open or reopen the backend; nothing in
	// this package renders it.
	Secret secret.Secret

	// Source is the redacted, human-readable location this share serves
	// from: a bucket and endpoint for s3, a container path for veracrypt,
	// the host path for local. Filled from the opener's Describe by Share
	// and Shares, never stored.
	Source string
}

// ShareDef is the internal spelling of Share, kept a distinct name so the
// config layer's registration surface is not the admin API's.
type ShareDef = Share

// ShareSpec describes what CreateShare is asked to produce.
type ShareSpec struct {
	Name string

	// Host is the on-disk path. Trusted server-side configuration; it must
	// never reach a client response. Required only for BackendLocal.
	Host string

	// Backend selects which package opens the new share's storage. Empty
	// reads as BackendLocal. Validated by ParseBackend, the trust boundary
	// for this string.
	Backend string

	// Config is the chosen backend's own JSON, already validated by that
	// backend's ParseConfig. Empty for BackendLocal.
	Config []byte

	// Secret is the one credential the chosen backend needs. Empty for
	// BackendLocal, which has none.
	Secret secret.Secret
}

// SharePatch is what UpdateShare accepts. Pointers separate an absent field from
// a cleared one, which is the difference between leaving trash untouched and
// turning it off.
type SharePatch struct {
	Name         *string
	Host         *string
	TrashEnabled *bool

	// Backend, present, must equal the share's current backend: repointing
	// a share at a different backend would leave every grant, share link
	// and cached identity naming data that is no longer there, so
	// UpdateShare refuses any other value with ErrUnprocessable.
	Backend *string

	// Config replaces the stored backend config wholesale when present.
	Config *[]byte

	// Secret replaces the stored credential when present. Absent leaves the
	// stored one alone; there is no way to spell "clear it" here, because a
	// non-local share with no credential cannot serve.
	Secret *secret.Secret
}

// The three backends this server can open a share against.
const (
	BackendLocal     = "local"
	BackendS3        = "s3"
	BackendVeracrypt = "veracrypt"
)

// ParseBackend validates an untrusted backend name, the trust boundary for
// every Backend string this package accepts from a spec, a patch or a
// stored row. Empty reads as BackendLocal, so a share created before
// backends existed and a client that omits the field both mean what they
// always meant.
func ParseBackend(s string) (string, error) {
	switch s {
	case "", BackendLocal:
		return BackendLocal, nil
	case BackendS3:
		return BackendS3, nil
	case BackendVeracrypt:
		return BackendVeracrypt, nil
	default:
		return "", fmt.Errorf("%q is not a share backend this server knows", s)
	}
}

// BackendOpener opens and describes a share's storage. core depends on this
// interface rather than importing objstore or vault directly, so the
// domain stays free of the HTTP client, the syscalls and the cryptography
// a non-local backend needs, and this package gains a new backend without
// a new import.
type BackendOpener interface {
	// Open brings up def's storage, or refuses. def.Secret carries the one
	// credential a non-local backend needs, already unsealed; def.Config
	// is that backend's own JSON.
	Open(ctx context.Context, def ShareDef) (vfs.Root, vfs.Admission, error)

	// Describe renders def's location for the admin screen, redacted: it
	// never carries a credential.
	Describe(def ShareDef) string
}

// localOpener treats every share as a local directory, which is every
// share's kind before this package learned any other. It is what
// Options.Backend defaults to when a caller supplies none, so every
// deployment and every existing test that never wires a backend package
// keeps working unchanged.
type localOpener struct{}

func (localOpener) Open(_ context.Context, def ShareDef) (vfs.Root, vfs.Admission, error) {
	return vfs.RegisterShareRoot(def.ID, def.Host, def.Policy)
}

func (localOpener) Describe(def ShareDef) string { return def.Host }

// shareEntry pairs a registered share's definition with its live root.
//
// root is nil for a broken share, and the entry remains in the map regardless.
// Removing it is what made a disk that never returned indistinguishable from a
// share someone deleted: missing from the admin list, missing from every
// account's roots, leaving only a line on a health endpoint.
type shareEntry struct {
	def       ShareDef
	root      vfs.Root
	brokenErr error
}

// RegisterShare opens the definition's storage through the injected
// BackendOpener and remembers it.
//
// The admission gate runs inside the opener, so a share this design cannot
// hold its contracts on is refused at registration rather than at the first
// operation that cannot keep them. Re-registering an id that is already
// registered replaces the entry, which is what reload, retry and edit all
// do.
func (c *Core) RegisterShare(ctx context.Context, def ShareDef) error {
	root, adm, err := c.backend.Open(ctx, def)
	if err != nil {
		return err
	}
	if adm.Warn != "" {
		c.warn("share admitted with a caveat",
			slog.String("share", def.Name), slog.String("warning", adm.Warn))
	}
	def.BrokenReason = ""
	c.replaceEntry(&shareEntry{def: def, root: root})
	return nil
}

// RegisterBroken remembers a share whose root would not open, marked with
// why.
func (c *Core) RegisterBroken(def ShareDef, cause error) {
	def.BrokenReason = RejectionKind(cause)
	c.replaceEntry(&shareEntry{def: def, brokenErr: cause})
}

// replaceEntry installs an entry and closes the root held by its predecessor.
//
// Closing is what makes repeated re-registration safe: both retry and edit pass
// through here, and without it every attempt would leak the descriptor opened by
// the last. The close runs after the lock is released, since it is a syscall
// that must not serialize every registry read behind it, and by then the entry
// is already unreachable through the map.
func (c *Core) replaceEntry(e *shareEntry) {
	c.sharesMu.Lock()
	old, had := c.shares[e.def.ID]
	c.shares[e.def.ID] = e
	c.sharesMu.Unlock()

	if !had || old.root == nil || old.root == e.root {
		return
	}
	if err := old.root.Close(); err != nil {
		c.warn("closing a replaced share's root failed",
			slog.String("share", old.def.Name), slog.Any("error", err))
	}
}

// ShareBroken explains why a share is unservable, returning nil when it is
// servable.
//
// Unregistered ids also yield nil: such a share is absent rather than broken,
// and callers needing to tell the two apart get false from the accessors.
func (c *Core) ShareBroken(id ShareID) error {
	c.sharesMu.RLock()
	defer c.sharesMu.RUnlock()
	e, ok := c.shares[id]
	if !ok {
		return nil
	}
	return e.brokenErr
}

// ProbeShares revalidates every registered share, moving each between live and
// broken, and returns only those that changed.
//
// Handling both directions is what justifies running this on a schedule. A root
// whose filesystem was unmounted beneath it retains a descriptor that opens
// nothing, so absent the probe the share fails one request at a time with
// nothing noticing. A broken share whose disk returned must resume working
// without anyone intervening.
//
// Retrying performs a complete re-registration rather than a re-open, so the
// admission gate runs again: a path that returned on a filesystem this server
// rejects remains broken.
func (c *Core) ProbeShares(ctx context.Context) (broke, healed []ShareDef) {
	for _, def := range c.Shares() {
		if c.ShareBroken(def.ID) != nil {
			if err := c.RegisterShare(ctx, def); err != nil {
				continue
			}
			def.BrokenReason = ""
			healed = append(healed, def)
			continue
		}

		root, ok := c.ShareRoot(def.ID)
		if !ok {
			continue
		}
		if err := root.Alive(); err != nil {
			c.RegisterBroken(def, err)
			def.BrokenReason = RejectionKind(err)
			broke = append(broke, def)
		}
	}
	return broke, healed
}

// UnregisterShare ceases serving a share and closes its root.
//
// The root is closed rather than merely discarded, since it is an open directory
// descriptor and a deployment adding and removing shares over its lifetime would
// otherwise leak one per removal. Broken entries have no root, and the nil check
// carries weight: dereferencing it is what made removing a share whose disk had
// disappeared return a 500, leaving the one share nothing will re-probe stuck in
// permanent degradation.
func (c *Core) UnregisterShare(id ShareID) {
	c.sharesMu.Lock()
	e, ok := c.shares[id]
	delete(c.shares, id)
	c.sharesMu.Unlock()

	if !ok || e.root == nil {
		return
	}
	if err := e.root.Close(); err != nil {
		c.warn("closing a removed share's root failed",
			slog.String("share", e.def.Name), slog.Any("error", err))
	}
}

// ShareRoot is the live root for an id, and false when it is unregistered or
// broken. A broken share hands out no root; handing out a nil one would move
// the failure to whoever dereferenced it.
func (c *Core) ShareRoot(id ShareID) (vfs.Root, bool) {
	c.sharesMu.RLock()
	defer c.sharesMu.RUnlock()
	e, ok := c.shares[id]
	if !ok || e.root == nil {
		return nil, false
	}
	return e.root, true
}

// shareEntry is the package-internal lookup Resolve uses, handing back the
// entry itself rather than a copy: the resolver needs the live root and the
// broken cause together, and reading them through two accessors could see
// two different registrations.
func (c *Core) shareEntry(id ShareID) (*shareEntry, bool) {
	c.sharesMu.RLock()
	defer c.sharesMu.RUnlock()
	e, ok := c.shares[id]
	return e, ok
}

// Share is the definition for a registered id, broken or not.
func (c *Core) Share(id ShareID) (ShareDef, bool) {
	c.sharesMu.RLock()
	e, ok := c.shares[id]
	c.sharesMu.RUnlock()
	if !ok {
		return ShareDef{}, false
	}
	def := e.def
	def.Source = c.backend.Describe(def)
	return def, true
}

// Shares lists every registered definition, broken included, by ascending
// id.
func (c *Core) Shares() []ShareDef {
	c.sharesMu.RLock()
	out := make([]ShareDef, 0, len(c.shares))
	for _, e := range c.shares {
		out = append(out, e.def)
	}
	c.sharesMu.RUnlock()

	for i := range out {
		out[i].Source = c.backend.Describe(out[i])
	}

	slices.SortFunc(out, func(a, b ShareDef) int {
		switch {
		case a.ID < b.ID:
			return -1
		case a.ID > b.ID:
			return 1
		default:
			return 0
		}
	})
	return out
}

// Roots is the user's virtual root: one entry per readable grant, labeled as
// the evaluator projects it, with the registry's own facts filled in.
//
// A broken share stays in the listing carrying why. Dropping it is what made
// a share disappear from the browser with no explanation anywhere a user
// could see.
func (c *Core) Roots(user UserID) []acl.RootEntry {
	// The same eager, best-effort hook Resolve runs. It belongs here too:
	// this listing is what a client draws as the top-level folder list, and
	// without it a new account sees no home until some later call happens to
	// resolve one.
	if err := c.ensureHome(context.Background(), user); err != nil {
		c.warn("creating a home directory failed; listing the other shares anyway",
			"user", int64(user), "error", err)
	}
	roots := c.labelledRoots(user)
	for i := range roots {
		id, err := num.Narrow[uint32](roots[i].Share)
		if err != nil {
			continue
		}
		def, ok := c.Share(ShareID(id))
		if !ok {
			continue
		}
		roots[i].TrashEnabled = def.TrashEnabled
		roots[i].SharedExternally = def.SharedExternally
		roots[i].BrokenReason = def.BrokenReason
	}
	return roots
}

// labelledRoots is the caller's roots as they are named everywhere.
//
// The evaluator holds grants, not share definitions, so an unlabelled grant
// over a share's whole root arrives carrying a generated placeholder. The
// share's own name is substituted here, once, because every reader of a label
// has to agree: the switcher drew the folder's name while the resolver still
// matched the placeholder, so clicking the folder the listing had just shown
// answered 404.
func (c *Core) labelledRoots(user UserID) []acl.RootEntry {
	roots := c.acl.Roots(int64(user))
	for i := range roots {
		if roots[i].Label != acl.GeneratedRootLabel(roots[i].Share) {
			continue
		}
		id, err := num.Narrow[uint32](roots[i].Share)
		if err != nil {
			continue
		}
		if def, ok := c.Share(ShareID(id)); ok && def.Name != "" {
			roots[i].Label = def.Name
		}
	}
	return roots
}

// RejectionKind is the health surface's token for why a share would not
// register. It is exported because the assembly layer registers shares too
// and carries the same tokens onto the health surface.
//
// "ungranted" is checked before the general vfs.ErrDenied case: a directory
// this build's own O_PATH open resolved, only to have the real read-open
// refused, is proof the sandbox itself withheld the path rather than the
// directory being genuinely unreadable. Folding the two together told an
// operator to check a mode and an owner that were already fine.
func RejectionKind(err error) string {
	var adm *vfs.AdmissionError
	switch {
	case errors.As(err, &adm):
		return adm.Type.String()
	case errors.Is(err, vfs.ErrNotFound):
		return "missing"
	case errors.Is(err, vfs.ErrSandboxDenied):
		return "ungranted"
	case errors.Is(err, vfs.ErrDenied):
		return "unreadable"
	default:
		return "unavailable"
	}
}

//go:build linux && compat_nc

// Package ncwire wires the compatibility layer to the rest of the server.
//
// It is the only package that sees both sides, which is why the SQL for
// compat-owned rows lives here rather than in the layer: the gate forbids the
// layer from importing the store, so the layer states what it needs through a
// port and this satisfies it.
//
// Everything here is behind the build tag, and the server's reference to it
// lives in one tagged file with a no-op sibling. A build without the tag does
// not compile these packages at all, which is stronger than a feature flag
// that still typechecks.
package ncwire

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"

	"github.com/heavycaffeiner/stowcloud/go/internal/auth"
	"github.com/heavycaffeiner/stowcloud/go/internal/clock"
	"github.com/heavycaffeiner/stowcloud/go/internal/compat/nc"
	"github.com/heavycaffeiner/stowcloud/go/internal/compat/ncport"
	"github.com/heavycaffeiner/stowcloud/go/internal/core"
	"github.com/heavycaffeiner/stowcloud/go/internal/num"
	"github.com/heavycaffeiner/stowcloud/go/internal/store/state"
	"github.com/heavycaffeiner/stowcloud/go/internal/vfs"
)

// Build assembles the layer from the server's own pieces.
//
// authSvc and origin carry the device login. A nil auth service leaves the
// flow unwired, which is what a build with no account service gets: the three
// login routes answer 404 rather than half a flow.
func Build(c *core.Core, st *state.DB, authSvc *auth.Service, origin string, clk clock.Clock, log *slog.Logger) *nc.Layer {
	if log == nil {
		log = slog.Default()
	}
	if clk == nil {
		clk = clock.System()
	}
	var flow *nc.LoginFlow
	if authSvc != nil && origin != "" {
		flow = nc.NewLoginFlow(flowStore{db: st}, authPort{svc: authSvc}, origin, clk.Nanos)
	}
	return nc.New(nc.Deps{
		FS:    fsPort{core: c},
		State: statePort{db: st},
		Caps:  defaultCaps(),
		Flow:  flow,
		// The chain has already resolved the principal by the time a mount
		// runs, so this reads it back rather than verifying anything itself.
		Authenticate: Authenticator,
		Warn:         func(msg string, args ...any) { log.Warn(msg, args...) },

		// The ports below stay nil until the core exposes what they need.
		// A nil port is not a silent gap: every surface behind one answers a
		// refusal a client can act on, and the tests hold that. Wiring a
		// half-built port would be worse, because the surface would answer
		// something a client would believe.
		//
		//   Accounts: the core has no account-info or quota reader yet.
		//   Search:   the search service has no per-user entry query yet.
		//   Preview:  the signing seam is Phase 9's and is not mounted yet.
		//   Direct:   depends on Preview.
	})
}

// defaultCaps is what the capabilities document reports.
//
// The two file-name lists have to match what the path layer actually refuses.
// Advertising a name as legal and then refusing the write makes the client
// retry the same file forever and the sync never converges, so they are read
// from the path layer rather than written out here.
func defaultCaps() nc.CapsConfig {
	return nc.CapsConfig{
		VersionMajor:  31,
		VersionMinor:  0,
		VersionMicro:  4,
		VersionString: "31.0.4",
		Edition:       "",

		PollIntervalSeconds: 60,

		// Advisory only: nothing server-side enforces a chunk ceiling, and an
		// oversized chunk is accepted normally. This says what an intermediary
		// is unlikely to reject, and it is the desktop client's own default.
		ChunkSizeAdvisory:     10 << 20,
		ChunkParallelAdvisory: 5,

		ShareeMinSearch: 3,

		BlacklistedFiles:            vfs.RefusedNames(),
		ForbiddenFilenameCharacters: vfs.RefusedNameCharacters(),

		ThemingName:  "Stowcloud",
		ThemingColor: "#0082c9",
		CanonicalURL: "",
	}
}

// fsPort satisfies the layer's file operations from the core.
type fsPort struct{ core *core.Core }

// Resolve parses the client path through the core, which is where path parsing
// lives. Doing it in the layer is the bug the two path types exist to prevent.
func (f fsPort) Resolve(user ncport.UserID, p string, need ncport.Perms) (ncport.Resolved, error) {
	vp, err := vfs.ParseVpath(p)
	if err != nil {
		return ncport.Resolved{}, core.ErrNotFound
	}
	return f.core.Resolve(user, vp, need)
}

func (f fsPort) List(ctx context.Context, r ncport.Resolved, cur ncport.Cursor) (ncport.Page, error) {
	return f.core.List(ctx, r, cur)
}

func (f fsPort) EntryAt(r ncport.Resolved, st ncport.Stat) ncport.Entry {
	return f.core.EntryAt(r, st)
}

func (f fsPort) Stat(r ncport.Resolved) (ncport.Stat, error) {
	return r.Root().Stat(r.Path())
}

// statePort satisfies the layer's storage from state.db.
type statePort struct{ db *state.DB }

// The compat-owned statements. They live here because the gate forbids the
// layer from importing the store, and as constants with parameters because
// every statement in this tree is.
const (
	selInstanceID = `SELECT value FROM compat_kv WHERE key = 'instance_id'`
	insInstanceID = `INSERT INTO compat_kv(key, value) VALUES ('instance_id', ?)
	                 ON CONFLICT(key) DO NOTHING`
)

// InstanceID returns the deployment's identity, minting one on first use.
//
// Minted rather than configured, and never regenerated: a client that saw one
// identity and then another treats the server as a different server and
// re-syncs everything it holds.
func (s statePort) InstanceID(ctx context.Context) (string, error) {
	var id string
	err := s.db.SQL().QueryRowContext(ctx, selInstanceID).Scan(&id)
	if err == nil {
		return id, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return "", fmt.Errorf("ncwire: reading the instance id: %w", err)
	}

	minted, merr := core.NewInstanceID()
	if merr != nil {
		return "", merr
	}
	// ON CONFLICT DO NOTHING, so two processes racing on first boot agree on
	// whichever landed rather than each keeping its own.
	if werr := s.db.Write(ctx, func(tx *sql.Tx) error {
		_, e := tx.ExecContext(ctx, insInstanceID, minted)
		return e
	}); werr != nil {
		return "", fmt.Errorf("ncwire: storing the instance id: %w", werr)
	}

	if rerr := s.db.SQL().QueryRowContext(ctx, selInstanceID).Scan(&id); rerr != nil {
		return "", fmt.Errorf("ncwire: reading back the instance id: %w", rerr)
	}
	return id, nil
}

func (s statePort) Favorites(ctx context.Context, user ncport.UserID) ([]ncport.Favorite, error) {
	rows, err := s.db.Favorites(ctx, int64(user))
	if err != nil {
		return nil, err
	}
	out := make([]ncport.Favorite, 0, len(rows))
	for _, r := range rows {
		// The share id comes off a row this build wrote, so a value that no
		// longer fits one is a corrupt row worth saying so about rather than
		// truncating into a different share.
		share, nerr := num.Narrow[uint32](r.Share)
		if nerr != nil {
			return nil, fmt.Errorf("ncwire: a stored favourite names share %d: %w", r.Share, nerr)
		}
		out = append(out, ncport.Favorite{
			Share: ncport.ShareID(share),
			Dev:   r.Dev,
			Ino:   r.Ino,
			Btime: r.Btime,
			Path:  r.Path,
		})
	}
	return out, nil
}

func (s statePort) SetFavorite(ctx context.Context, user ncport.UserID, f ncport.Favorite, on bool) error {
	return s.db.SetFavorite(ctx, int64(user), state.Favorite{
		Share: int64(f.Share),
		Dev:   f.Dev,
		Ino:   f.Ino,
		Btime: f.Btime,
		Path:  f.Path,
	}, on)
}

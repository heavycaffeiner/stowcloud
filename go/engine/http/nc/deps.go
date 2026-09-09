//go:build linux && compat_nc

package nc

import (
	"context"
	"log/slog"

	"github.com/gofiber/fiber/v2"

	"github.com/heavycaffeiner/stowcloud/go/engine/kit/clock"
	"github.com/heavycaffeiner/stowcloud/go/engine/service/acl"
	"github.com/heavycaffeiner/stowcloud/go/engine/service/auth"
	"github.com/heavycaffeiner/stowcloud/go/engine/service/core"
	"github.com/heavycaffeiner/stowcloud/go/engine/service/preview"
	"github.com/heavycaffeiner/stowcloud/go/engine/service/search/svc"
	"github.com/heavycaffeiner/stowcloud/go/engine/service/upload"
)

// What this surface needs from the rest of the engine, and the one type that
// holds it.
//
// The services arrive as themselves. The three things that do not are the
// store-backed facts (a stable file id, the favourite set, which shares are
// encrypted), the device-login flow and the base URL a request arrived on:
// each lives in a tier this package may not import, so each crosses as a
// contract stated here and satisfied by the assembly.

// Store is the store-backed half of the surface.
//
// Every method is a question this package cannot answer from a service: an
// identity minted in a cache table, a favourite row, the encrypted-share set.
// A deployment always has one, so the interface carries no nil case.
type Store interface {
	// FileID is the stable numeric identity a client's sync journal is keyed
	// on. It must answer the same value for the same file across restarts,
	// and must not allocate: a listing that minted ids would hand out an
	// identity for a file that may be gone by the time the client acts on it.
	FileID(ctx context.Context, e core.Entry) (uint64, error)

	// Favorites reads the caller's starred set once, for a request that asks
	// about many entries.
	Favorites(ctx context.Context, user core.UserID) (FavoriteSet, error)

	// SetFavorite stars or unstars one entry.
	SetFavorite(ctx context.Context, user core.UserID, e core.Entry, on bool) error

	// EncryptedShares names the shares whose contents this surface must not
	// reveal. A client here has no way to decrypt one, so a listing that
	// showed it would present ciphertext as the user's files.
	EncryptedShares(ctx context.Context) (map[core.ShareID]bool, error)
}

// FavoriteSet is one account's starred set, as one request sees it.
type FavoriteSet interface {
	// Has reports whether an entry is starred. Keyed on the entry's identity
	// rather than its path, so a starred file keeps its star across a rename.
	Has(e core.Entry) bool
	// List names every starred path, for the query that answers the whole set.
	List() []Favorite
}

// Favorite is one starred path.
type Favorite struct {
	Share core.ShareID
	// Path is share-relative, as the store recorded it.
	Path string
}

// Flow is the device-login flow: a client opens a page, a person approves, and
// the client collects a credential by polling.
type Flow interface {
	// Begin mints the two tokens: one the client polls with, one the person's
	// browser approves. The URLs built from them are this package's own
	// spelling, so nothing below it has to know them.
	Begin(ctx context.Context, origin string) (FlowTokens, error)
	// Approve records a person's consent against the login token.
	Approve(ctx context.Context, loginToken string, user int64, login string) error
	// Poll delivers the credential once consent is recorded. A flow still
	// waiting answers ErrFlowPending, which is the "not yet" a client polls
	// against rather than an error.
	Poll(ctx context.Context, pollToken, origin string) (FlowDelivery, error)
}

// FlowTokens is what Begin produced.
type FlowTokens struct {
	PollToken  string
	LoginToken string
}

// FlowDelivery is the credential a completed flow hands over.
type FlowDelivery struct {
	Server      string
	LoginName   string
	AppPassword string
}

// OriginRequest is what the base-URL renderer is told about a request.
//
// The renderer decides which of these to trust, because that decision depends
// on the deployment's declared names and its trusted-proxy set, neither of
// which this package can see.
type OriginRequest struct {
	Host           string
	ForwardedHost  string
	ForwardedProto string
	PeerAddr       string
	TLS            bool
}

// Deps is everything the surface is built over.
type Deps struct {
	Core  *core.Core
	Auth  *auth.Service
	Store Store

	// Uploads is nil in a deployment without a resumable upload engine, and
	// the chunked upload collection then refuses every method rather than
	// accepting chunks it could never assemble.
	Uploads *upload.Engine
	// Preview is nil where no decoder is configured. A thumbnail request then
	// answers 404, which every client renders as its own placeholder icon.
	Preview *preview.Service
	// Search is nil where no index is wired; the unified search endpoints then
	// answer an empty result rather than a refusal, because a client shows a
	// refusal as a broken account.
	Search *svc.Service
	// Flow is nil where device login is disabled.
	Flow Flow

	// Features reports what to advertise. Read per request: an operator can
	// turn thumbnails off while a client is running, and the document a client
	// re-reads has to say so.
	Features func() Features
	// Origin renders the base URL a request arrived on, without a trailing
	// slash. Every absolute URL this surface hands a client is built from it.
	Origin func(OriginRequest) string
	// ConsentPage answers the page a person approves a device login on.
	//
	// A whole handler rather than a document: the page carries a script nonce,
	// a CSRF value derived from the caller's own session cookie, and a
	// redirect for a visitor who has not signed in yet. All three are the
	// assembly's to decide, and none of them survives being reduced to a byte
	// slice. This package owns only the URL it answers on. Nil answers 404.
	ConsentPage fiber.Handler

	// Resolve turns one client path into a capability, with no credential
	// scope applied: this package narrows the result by the caller's own mask
	// afterwards.
	//
	// A seam rather than a direct call, because parsing a path is the
	// filesystem tier's own work and the presentation tier may not name its
	// types. The same seam the native protocol mount uses.
	Resolve func(user core.UserID, path string, need acl.Perms) (core.Resolved, error)

	// VpathOf crosses a share and a share-relative path back into the path a
	// client addresses, which is the reverse direction a stored row (a
	// favourite, a journal entry) has to be rendered through.
	VpathOf func(user core.UserID, share core.ShareID, sharePath string) (string, error)

	// LocateFile maps a stable file id back to the path the caller addresses
	// it by. A client opening a notification, a preview or a direct link
	// names a file by id and nothing else, so without this the whole of that
	// is unreachable. The reverse index lives in a tier this package may not
	// import. Nil answers every id as absent.
	LocateFile func(ctx context.Context, user core.UserID, fileID uint64) (string, error)

	// SealClaim mints the short-lived capability a direct media URL carries,
	// and OpenClaim reads one back. Both are the assembly's: the key is one
	// the deployment holds and rotates, and a token this package invented
	// would be a second credential format nothing else can revoke. Nil
	// disables the direct-URL endpoints.
	SealClaim func(user core.UserID, path string) (string, error)
	OpenClaim func(token string) (user core.UserID, path string, err error)

	// PublicLinkPath is where a link token is served from, as a path with a
	// leading slash. The engine's own public link surface owns that spelling,
	// and a share whose URL points anywhere else is a link that opens
	// nothing.
	PublicLinkPath func(token string) string

	// LockGuard reports whether a foreign lock blocks a write at a path. Nil
	// admits every write, which is what a deployment without a lock table
	// answers.
	LockGuard func(ctx context.Context, res core.Resolved, principal int64) error

	Clock  clock.Clock
	Logger *slog.Logger
}

// Server answers the surface.
type Server struct {
	deps Deps
	log  *slog.Logger
	clk  clock.Clock
}

// New builds the server. A missing logger or clock is filled in rather than
// refused: neither is a deployment decision, and a nil one is a panic in a
// request path instead of a configuration error at boot.
func New(d Deps) *Server {
	log := d.Logger
	if log == nil {
		log = slog.Default()
	}
	clk := d.Clock
	if clk == nil {
		clk = clock.System()
	}
	if d.Features == nil {
		d.Features = func() Features { return Features{} }
	}
	return &Server{deps: d, log: log.With("subsystem", "nc"), clk: clk}
}

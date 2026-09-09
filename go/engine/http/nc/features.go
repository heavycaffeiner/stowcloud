//go:build linux && compat_nc

package nc

// Features is what the capabilities document advertises.
//
// Every field is something this deployment either has or does not: a decoder
// for thumbnails, an index for search, an upload engine for chunked
// transfers. None of it is a preference. A client reads the document once at
// sign-in and again on reconnect, and turns whole screens off on the strength
// of it, so advertising something absent is a screen that fails rather than
// one that is missing.
type Features struct {
	// Version is the server version string a client compares against its own
	// feature gates. It has to satisfy the gates for the features advertised
	// below, because a client checks the number before it uses an endpoint.
	Version string
	// InstanceID is the deployment's stable identity, minted once. A client
	// that saw one value and then another re-syncs everything it holds.
	InstanceID string

	// Thumbnails reports whether preview generation is available.
	Thumbnails bool
	// Search reports whether the unified search endpoints answer.
	Search bool
	// Trash reports whether deletes are recoverable, which decides whether a
	// client offers its own trash screen.
	Trash bool
	// Chunking reports whether the resumable upload engine is wired. Without
	// it a large upload has no path that can succeed.
	Chunking bool
	// Favorites reports whether the starred set is stored.
	Favorites bool

	// Sharing reports whether the share API answers at all.
	Sharing bool
	// PublicLinks reports whether a link share can be minted.
	PublicLinks bool
	// PublicUpload reports whether a link may accept uploads.
	PublicUpload bool
	// LinkPasswordEnforced reports that a link without a password is refused,
	// which makes the client's own password field mandatory.
	LinkPasswordEnforced bool
	// LinkExpiryEnforced reports that a link must carry an expiry, with
	// LinkExpiryDays as the maximum. A client that is told this sets its own
	// date picker's bounds from it.
	LinkExpiryEnforced bool
	LinkExpiryDays     int
	// DefaultSharePerms is the permission mask a client pre-selects when it
	// offers to share something.
	DefaultSharePerms int64
}

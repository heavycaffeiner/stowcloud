//go:build linux && compat_nc

// Package nc serves the HTTP surface another product's clients address.
//
// One package owns the whole vocabulary: the OCS envelope, the capabilities
// document, the share wire shape, the DAV property names, the chunked upload
// collection and the URL layouts. Nothing outside it spells "ocs",
// "remote.php" or an "oc:" property name, and nothing inside it reaches for
// the engine's own wire shapes. That boundary is the point of the package.
//
// The engine's native WebDAV mount is a separate implementation on purpose.
// The clients this package serves parse a multistatus by matching literal
// element prefixes, read only the first propstat block of a response, and
// refuse an entry missing an etag, a file id or a permission string. Those are
// not options a generic protocol handler can carry as flags: they decide how
// every response is written. Serving both from one renderer meant every fix
// for one client was a risk to the other.
//
// A handler here never resolves a permission itself. It takes the principal
// the middleware chain resolved, asks the core to resolve a path with the
// permission the operation needs, and renders whatever comes back. The core
// answers the same refusal for a path the caller may not see as for one that
// does not exist, so no handler has to decide which of the two it is looking
// at.
package nc

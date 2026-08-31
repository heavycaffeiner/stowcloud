//go:build linux

package spa

// The CSP sources for the bundle's own inline scripts.
//
// The framework writes its hydration bootstrap as an inline script rather
// than a file, because the bootstrap carries the data the page was rendered
// with. A policy of `script-src 'self'` blocks it, and the failure is the
// quiet kind: no request fails and no status is wrong, the document arrives
// whole, and the browser shows a blank page with the reason buried in the
// console. That page was shipped once.
//
// The alternative, 'unsafe-inline', admits every inline script on every page
// the server answers, including any a stored file manages to get reflected
// into. A hash admits exactly the bytes the build produced.
//
// The hashes are computed from the embedded document rather than written
// down. A constant would be a second copy of a decision the build already
// makes, and the two diverge on the first frontend change that touches the
// bootstrap: the filenames still match, nothing looks wrong, and the page is
// blank again.

// InlineScriptHashList returns one bare source per inline script the
// bundle's document carries, in the "sha256-base64" form the policy builder
// takes: it adds the quotes itself, and a source that arrived pre-quoted
// would be quoted twice and dropped by the browser.
//
// The list is empty without a bundle, which leaves the policy exactly as
// strict: a server with no interface has no inline script to admit.
func InlineScriptHashList() []string { return inlineScriptHashes() }

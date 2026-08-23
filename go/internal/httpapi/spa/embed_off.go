//go:build !embed_ui

package spa

import "net/http"

// handler reports that this build carries no frontend.
//
// The no-op sibling exists so every other package can call Handler
// unconditionally: a build tag that changes a function's existence rather than
// its behaviour pushes the tag into every caller.
func handler() (http.Handler, bool) { return nil, false }

// inlineScriptHashes is empty without a bundle: there is no document, so there
// is no inline script, and the policy has nothing to admit.
func inlineScriptHashes() string { return "" }

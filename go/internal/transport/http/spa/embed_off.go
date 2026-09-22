//go:build !embed_ui

package spa

import "net/http"

// handler reports no interface. The twin exists so callers can invoke
// Handler without knowing the tag: a tag that removed the function instead
// of changing what it returns would spread itself across every caller.
func handler() (http.Handler, bool) { return nil, false }

// inlineScriptHashes is empty: with no document embedded there is nothing to
// admit, and the policy keeps whatever strictness it already had.
func inlineScriptHashes() []string { return nil }

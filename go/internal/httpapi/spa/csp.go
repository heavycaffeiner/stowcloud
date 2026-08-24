package spa

import (
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"strings"
)

// The hash sources the CSP needs for the bundle's own inline scripts.
//
// The frontend's entry point is an inline script: the framework writes the
// hydration bootstrap into the document rather than into a file, because it
// carries the data the page was rendered with. A policy of `script-src 'self'`
// blocks it, and what a browser then shows is a blank page. The console says
// why, and nothing else does: no request fails, no status is wrong, and the
// document itself arrives intact. That is the failure this exists to prevent,
// and it is the one that was shipped.
//
// The alternative is 'unsafe-inline', which would admit every inline script on
// every page this server serves, including any that a stored file managed to
// get reflected into one. A hash admits exactly the bytes that were built.
//
// The hashes are computed from the embedded bundle at startup rather than
// written down. A constant would be a second copy of something the build
// already decides, and the two would disagree on the first frontend change
// that touched the bootstrap: the filenames would still match, so nothing
// would look wrong, and the page would be blank again.

// InlineScriptHashes returns the CSP source list for every inline script in
// the bundle's document, space-joined and ready to concatenate into a policy.
//
// Empty when this build carries no bundle, which leaves the policy as strict
// as it was: a server with no frontend has no inline script to admit.
func InlineScriptHashes() string { return inlineScriptHashes() }

// hashesFrom scans an HTML document and returns one quoted sha256 source per
// inline script, in document order.
//
// A script with a src attribute is not inline: it is fetched, and 'self'
// already covers it. Only the ones whose body is in the document need a hash,
// and the hash is over exactly the bytes between the tags, which is what the
// browser computes.
func hashesFrom(html string) []string {
	var out []string
	lower := strings.ToLower(html)

	for i := 0; ; {
		open := strings.Index(lower[i:], "<script")
		if open < 0 {
			break
		}
		open += i
		gt := strings.Index(lower[open:], ">")
		if gt < 0 {
			break
		}
		gt += open

		bodyStart := gt + 1
		close := strings.Index(lower[bodyStart:], "</script>")
		if close < 0 {
			break
		}
		close += bodyStart

		if !strings.Contains(lower[open:gt], "src=") {
			sum := sha256.Sum256([]byte(html[bodyStart:close]))
			out = append(out, fmt.Sprintf("'sha256-%s'", base64.StdEncoding.EncodeToString(sum[:])))
		}
		i = close + len("</script>")
	}
	return out
}

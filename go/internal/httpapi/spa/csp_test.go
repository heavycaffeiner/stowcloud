package spa

import (
	"strings"
	"testing"
)

// The inline-script hashes.
//
// What these protect is a failure with no symptom: the framework's hydration
// bootstrap is an inline script, a policy of `script-src 'self'` blocks it, and
// the browser renders a blank page. Every request succeeds, the document
// arrives whole, and the only evidence is a console line nobody sees on a
// phone. It was shipped exactly that way.

func TestAnInlineScriptIsHashedAndAScriptWithSrcIsNot(t *testing.T) {
	const doc = `<!doctype html><html><head>` +
		`<script src="/app/entry.js"></script>` +
		`<script>console.log("inline")</script>` +
		`</head><body></body></html>`

	got := hashesFrom(doc)
	if len(got) != 1 {
		t.Fatalf("got %d hashes, want one: the src script is fetched and 'self' covers it", len(got))
	}
	if !strings.HasPrefix(got[0], "'sha256-") || !strings.HasSuffix(got[0], "'") {
		t.Fatalf("the hash is %q, want a quoted sha256 source", got[0])
	}
}

// The hash is over the bytes between the tags, which is what the browser
// computes. A hash over anything else is a policy that blocks the script it
// was meant to admit.
func TestTheHashIsOverTheScriptBodyExactly(t *testing.T) {
	// The base64 sha256 of the three bytes `x=1`, computed outside this
	// package and agreed on by two other implementations: the repository's own
	// scripts/inline-script-hashes.mjs, and python's hashlib. A test that took
	// its expectation from this code would pass against any hashing it happened
	// to do, including the wrong one.
	const want = "'sha256-HyBrEcI+KMwlDe1/wAmNOCOoRnpUNA8axOU1y4VEST8='"

	got := hashesFrom(`<html><script>x=1</script></html>`)
	if len(got) != 1 || got[0] != want {
		t.Fatalf("got %v, want [%s]", got, want)
	}
}

// Multiple inline scripts each get a hash, in document order.
func TestEveryInlineScriptIsHashed(t *testing.T) {
	got := hashesFrom(`<script>a</script><script src="x.js"></script><script>b</script>`)
	if len(got) != 2 {
		t.Fatalf("got %d hashes, want two", len(got))
	}
	if got[0] == got[1] {
		t.Fatal("two different scripts hashed the same")
	}
}

// A malformed document must not hang or panic: it is a build artefact rather
// than request input, but a scanner that loops on an unterminated tag is a
// server that does not start.
func TestAMalformedDocumentTerminates(t *testing.T) {
	for _, doc := range []string{
		"<script",
		"<script>",
		"<script>unterminated",
		"<script src=",
		"",
	} {
		hashesFrom(doc) // must return
	}
}

// The attribute test is case-insensitive and tolerates whitespace, because a
// bundler is free to emit either. Treating `SRC=` as inline would hash a
// script that has no body and admit nothing useful, while the real inline one
// went unhashed.
func TestTheSrcAttributeIsMatchedWhateverItsCase(t *testing.T) {
	if got := hashesFrom(`<SCRIPT SRC="/app/x.js"></SCRIPT>`); len(got) != 0 {
		t.Fatalf("an uppercase src script was hashed as inline: %v", got)
	}
}

// The real bundle, when this build has one. The policy is assembled from this,
// so an empty answer here is the blank page.
func TestTheBundlesOwnBootstrapIsHashed(t *testing.T) {
	if _, ok := Handler(); !ok {
		t.Skip("this build carries no frontend")
	}
	if InlineScriptHashes() == "" {
		t.Fatal("the bundle produced no hash, so its inline bootstrap would be blocked")
	}
}

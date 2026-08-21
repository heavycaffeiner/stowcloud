package main

import (
	"net/http"
	"os"
	"strings"
)

// The corpus placeholders.
//
// A bound's value belongs to the server, so the corpus names the bound and this
// expands it. Writing the number into the corpus would mean the corpus and the
// server disagree the first time either moves, and the corpus would be the one
// nobody checks.

// The placeholders and what they expand to. A body at a bound and one past it
// have to be built rather than typed, because a megabyte of literal text in a
// checked-in file is a file nobody can read.
func expand(s string) string {
	if !strings.Contains(s, "@@") {
		return s
	}
	for name, value := range placeholders() {
		s = strings.ReplaceAll(s, name, value())
	}
	return s
}

// placeholders is a map of thunks, so a megabyte is built only when a request
// actually names it.
func placeholders() map[string]func() string {
	return map[string]func() string{
		// The general request-body bound, at it and one past.
		"@@BODY_AT_LIMIT@@":   func() string { return jsonPadding(requestBodyLimit) },
		"@@BODY_PAST_LIMIT@@": func() string { return jsonPadding(requestBodyLimit + 1) },

		// The XML body bound, which is lower because an XML body is parsed
		// rather than streamed to disk.
		"@@XML_AT_LIMIT@@":   func() string { return xmlPadding(xmlBodyLimit) },
		"@@XML_PAST_LIMIT@@": func() string { return xmlPadding(xmlBodyLimit + 1) },

		// The path bounds.
		"@@DEEP_AT_LIMIT@@":   func() string { return deepPath(pathComponents - 1) },
		"@@DEEP_PAST_LIMIT@@": func() string { return deepPath(pathComponents + 1) },
		"@@NAME_AT_LIMIT@@":   func() string { return strings.Repeat("n", nameBytes) },
		"@@NAME_PAST_LIMIT@@": func() string { return strings.Repeat("n", nameBytes+1) },

		// The XML nesting bound, one past it.
		"@@DEEP_XML@@": func() string { return deepXML(xmlDepth + 1) },

		// The content origin, which the corpus cannot know.
		"@@CONTENT_HOST@@": func() string { return envOr("SC_DIFFER_CONTENT_HOST", "content.localhost") },
	}
}

// The bounds, transcribed from the server's own limits package.
//
// Transcribed rather than imported: this tool talks to two servers over HTTP
// and importing one of them would make the corpus agree with that build by
// construction, which is the comparison it exists to make.
const (
	requestBodyLimit = 1 << 20
	xmlBodyLimit     = 256 << 10
	pathComponents   = 256
	nameBytes        = 255
	xmlDepth         = 64
)

// jsonPadding builds a syntactically valid body of exactly n bytes, so what is
// being tested is the bound rather than the parser.
func jsonPadding(n int) string {
	const prefix = `{"path":"docs/x","pad":"`
	const suffix = `"}`
	fill := n - len(prefix) - len(suffix)
	if fill < 0 {
		fill = 0
	}
	return prefix + strings.Repeat("p", fill) + suffix
}

func xmlPadding(n int) string {
	const prefix = `<?xml version="1.0"?><D:propfind xmlns:D="DAV:"><D:prop><D:displayname>`
	const suffix = `</D:displayname></D:prop></D:propfind>`
	fill := n - len(prefix) - len(suffix)
	if fill < 0 {
		fill = 0
	}
	return prefix + strings.Repeat("p", fill) + suffix
}

func deepPath(components int) string {
	parts := make([]string, components)
	for i := range parts {
		parts[i] = "d"
	}
	return strings.Join(parts, "/")
}

func deepXML(depth int) string {
	var b []byte
	b = append(b, `<?xml version="1.0"?><D:propfind xmlns:D="DAV:">`...)
	for range depth {
		b = append(b, "<D:x>"...)
	}
	for range depth {
		b = append(b, "</D:x>"...)
	}
	b = append(b, "</D:propfind>"...)
	return string(b)
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

// applyAuth attaches the credential a corpus entry asked for.
//
// The values come from the environment because they are minted per run: a
// session token written into the corpus is a token that is wrong on every run
// but the one that produced it.
func applyAuth(req *http.Request, kind string, cred credential) {
	switch kind {
	case "", "none":
		return
	case "session", "session-no-origin":
		// The cookie arrives as name=value, per side, because the two
		// implementations do not agree on the name: one carries the
		// host-locked prefix and the other does not. One value for both sends
		// each side something it ignores, and two unauthenticated answers
		// match.
		name, value, ok := strings.Cut(cred.Cookie, "=")
		if !ok || value == "" {
			return
		}
		req.AddCookie(&http.Cookie{Name: name, Value: value})
		if cred.CSRF != "" {
			req.Header.Set("Sc-Csrf", cred.CSRF)
		}
		if kind == "session" && req.Header.Get("Origin") == "" {
			req.Header.Set("Origin", "https://"+req.Host)
		}
	case "bad-session":
		req.AddCookie(&http.Cookie{Name: "sc_session", Value: strings.Repeat("0", 64)})
	case "expired-session":
		if token := os.Getenv("SC_DIFFER_EXPIRED_SESSION"); token != "" {
			req.AddCookie(&http.Cookie{Name: "sc_session", Value: token})
		}
	}
}

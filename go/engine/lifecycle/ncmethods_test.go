//go:build linux && compat_nc

package lifecycle_test

import (
	"strings"
	"testing"
)

// Every method this surface advertises, against every kind of resource it
// has.
//
// The defect this table exists for was one verb against one kind that no
// other test covered: a folder answered a read as a method it does not allow,
// and the client that probes a folder that way before every upload abandoned
// the transfer. The combinations are cheap to enumerate and a surprise in any
// of them is now a diff here rather than a report from somebody whose upload
// failed.
//
// A status recorded here is a claim about what a client is told, not about
// what the standard permits. Where the two differ the comment says which
// client reads it and what it does with it.
func TestTheMethodTable(t *testing.T) {
	t.Parallel()
	f := newNCFixture(t, []byte("bytes"))

	const (
		propfind = `<?xml version="1.0"?><d:propfind xmlns:d="DAV:"><d:prop><d:getetag/></d:prop></d:propfind>`
		patch    = `<?xml version="1.0"?><d:propertyupdate xmlns:d="DAV:" xmlns:oc="http://owncloud.org/ns">` +
			`<d:set><d:prop><oc:favorite>1</oc:favorite></d:prop></d:set></d:propertyupdate>`
		report = `<?xml version="1.0"?><oc:filter-files xmlns:d="DAV:" xmlns:oc="http://owncloud.org/ns">` +
			`<d:prop><d:getetag/></d:prop><oc:filter-rules><oc:favorite>1</oc:favorite></oc:filter-rules>` +
			`</oc:filter-files>`
		search = `<?xml version="1.0"?><d:searchrequest xmlns:d="DAV:"><d:basicsearch>` +
			`<d:select><d:prop><d:getetag/></d:prop></d:select>` +
			`<d:from><d:scope><d:href>/files/alice</d:href><d:depth>infinity</d:depth></d:scope></d:from>` +
			`<d:where><d:like><d:prop><d:displayname/></d:prop><d:literal>%x%</d:literal></d:like></d:where>` +
			`</d:basicsearch></d:searchrequest>`
	)

	// One target per kind of thing this tree holds. Each row below names the
	// kind rather than the path, so a reader sees what is being claimed.
	targets := map[string]string{
		"account root": f.davRoot(),
		"share root":   f.filePath(""),
		"folder":       f.filePath("sub"),
		"file":         f.filePath("doc.bin"),
		"absent":       f.filePath("nothing-here.bin"),
		// A name no other row touches, so the create below is a create and
		// not a collision with whatever ran before it.
		"unused name": f.filePath("made-by-the-table"),
	}

	cases := []struct {
		method string
		kind   string
		body   string
		header map[string]string
		want   int
		why    string
	}{
		// A read of anything that exists answers 200. A folder carries no
		// body, but one client probes a folder with HEAD before every upload
		// into it and accepts only 200.
		{"GET", "file", "", nil, 200, ""},
		{"HEAD", "file", "", nil, 200, ""},
		{"GET", "folder", "", nil, 200, "the probe that precedes an upload"},
		{"HEAD", "folder", "", nil, 200, "the probe that precedes an upload"},
		{"GET", "share root", "", nil, 200, "the probe that precedes an upload"},
		{"HEAD", "share root", "", nil, 200, "the probe that precedes an upload"},
		{"GET", "account root", "", nil, 200, "a client confirms the mount answers"},
		{"HEAD", "account root", "", nil, 200, "a client confirms the mount answers"},
		{"GET", "absent", "", nil, 404, ""},
		{"HEAD", "absent", "", nil, 404, ""},

		// Listing. The account root has no resource behind it and is
		// projected from the shares the caller may reach.
		{"PROPFIND", "account root", propfind, map[string]string{"Depth": "1"}, 207, ""},
		{"PROPFIND", "share root", propfind, map[string]string{"Depth": "1"}, 207, ""},
		{"PROPFIND", "folder", propfind, map[string]string{"Depth": "1"}, 207, ""},
		{"PROPFIND", "file", propfind, map[string]string{"Depth": "0"}, 207, ""},
		{"PROPFIND", "absent", propfind, map[string]string{"Depth": "0"}, 404, ""},

		// Starring. The account root stores nothing of its own.
		{"PROPPATCH", "file", patch, nil, 207, ""},
		{"PROPPATCH", "folder", patch, nil, 207, ""},
		{"PROPPATCH", "account root", patch, nil, 405, "nothing to store a property against"},

		// The two queries, which one client will not issue at all unless
		// OPTIONS advertises them.
		{"REPORT", "account root", report, nil, 207, ""},
		{"REPORT", "share root", report, nil, 207, ""},
		{"SEARCH", "account root", search, nil, 207, ""},

		// Writing. A create at the account root has nowhere to land: this
		// server's root is the list of shares, not a folder.
		{"PUT", "file", "replaced", nil, 204, "a replace, not a create"},
		{"PUT", "absent", "created", nil, 201, ""},
		{"PUT", "account root", "nowhere", nil, 405, "the root is a collection, and a PUT never asks to remove a tree"},
		{"PUT", "share root", "onto a folder", nil, 405, "a PUT never asks to remove a tree"},
		{"MKCOL", "account root", "", nil, 405, "the root is projected, not stored"},
		{"MKCOL", "share root", "", nil, 405, "one client maps this to already-there"},
		{"MKCOL", "unused name", "", nil, 201, ""},

		// Locking is advertised because one client probes for it, and
		// answered honestly because this surface has no lock table.
		{"LOCK", "file", "", nil, 501, "advertised for the probe, refused honestly"},
		{"UNLOCK", "file", "", nil, 204, "a client with no token has nothing to release"},
	}

	for _, c := range cases {
		url, ok := targets[c.kind]
		if !ok {
			t.Fatalf("no target for %q", c.kind)
		}
		var body *strings.Reader
		if c.body != "" {
			body = strings.NewReader(c.body)
		}
		headers := map[string]string{"Content-Type": "application/xml"}
		for k, v := range c.header {
			headers[k] = v
		}

		var resp ncReply
		var got []byte
		if body == nil {
			resp, got = f.request(t, c.method, url, nil, headers)
		} else {
			resp, got = f.request(t, c.method, url, body, headers)
		}
		if resp.StatusCode != c.want {
			t.Errorf("%s on the %s answered %d, want %d (%s)\n%s",
				c.method, c.kind, resp.StatusCode, c.want, c.why, got)
		}
	}
}

// The chunked upload collection, over the destinations a client can name.
//
// A destination that cannot hold a file is refused when the session is
// created, before any chunk is accepted: a session that took bytes it could
// never publish would report progress and then fail at the end, after the
// whole file had been sent.
func TestTheUploadCollectionRefusesADestinationItCannotPublishTo(t *testing.T) {
	t.Parallel()
	f := newNCFixture(t, []byte("bytes"))

	for _, c := range []struct {
		what        string
		destination string
		want        int
	}{
		{"a file inside a share", f.filePath("large.bin"), 201},
		{"a share root", f.filePath(""), 400},
		{"the account root", f.davRoot(), 404},
		{"an unknown share", f.filePath("no-such-share/x.bin"), 404},
	} {
		session := f.base + "/remote.php/dav/uploads/" + f.login + "/session-" + strings.ReplaceAll(c.what, " ", "-")
		resp, body := f.request(t, "MKCOL", session, nil,
			map[string]string{"Destination": c.destination})
		if resp.StatusCode != c.want {
			t.Errorf("a session for %s answered %d, want %d\n%s",
				c.what, resp.StatusCode, c.want, body)
		}
	}
}

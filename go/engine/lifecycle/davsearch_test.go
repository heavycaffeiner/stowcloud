//go:build linux

package lifecycle_test

import (
	"context"
	"encoding/xml"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/heavycaffeiner/stowcloud/go/engine/http/dav"
	"github.com/heavycaffeiner/stowcloud/go/engine/infra/vfs"
	"github.com/heavycaffeiner/stowcloud/go/engine/service/core"
)

// The query surface, driven through the mount.
//
// The mobile clients' favourites view is a REPORT, so what matters here is
// that a filter reaches a source, the source's hits come back with their
// properties, and a vocabulary nobody claims is refused rather than answered
// as empty.

// entryAt builds the entry a test wants a source to report.
func (f *fixture) entryAt(t *testing.T, path string) core.Entry {
	t.Helper()
	res := f.resolve(t, path)
	st, err := res.Root().Stat(res.Path())
	if err != nil {
		t.Fatalf("statting %q: %v", path, err)
	}
	return f.core.EntryAt(res, st)
}

// errUnacceptableTerm is what the source answers a filter it cannot apply.
var errUnacceptableTerm = errors.New("a term this source does not apply")

// favSourceWith builds a handler with the favourites source wired.
func favSourceWith(t *testing.T, f *fixture, hits []core.Entry) http.Handler {
	t.Helper()
	f.h = dav.New(dav.Options{
		Core:  f.core,
		Taker: f.real,
		Sources: []dav.QuerySource{
			&favSourceFixture{fixture: f, hits: hits},
		},
	})
	return f.mounted()
}

// favSourceFixture carries the hits a test wants back, so the source does not
// need its own index: what is under test is the dispatch, not the matching.
type favSourceFixture struct {
	fixture *fixture
	hits    []core.Entry
}

func (s *favSourceFixture) Namespaces() []string {
	return []string{"http://nextcloud.org/ns"}
}

func (s *favSourceFixture) Query(
	ctx context.Context, res core.Resolved, leaves []dav.Leaf, want []xml.Name,
) ([]core.Entry, error) {
	if len(leaves) != 1 || leaves[0].Name.Local != "is-favorite" || leaves[0].Value != "1" {
		return nil, errUnacceptableTerm
	}
	// Permission-scoped like any listing: a hit the caller cannot read now is
	// not a hit. Re-resolving each is what applies the caller's current
	// grants, which a grant that begins partway down a tree requires.
	var out []core.Entry
	for _, e := range s.hits {
		vp, err := vfs.ParseVpath("/files/" + strings.TrimPrefix(e.Path.String(), "/"))
		if err != nil {
			continue
		}
		if _, rerr := s.fixture.core.Resolve(core.UserID(1), vp, 0); rerr != nil {
			continue
		}
		out = append(out, e)
	}
	return out, nil
}

const favReport = `<?xml version="1.0"?>
<oc:filter-files xmlns:d="DAV:" xmlns:oc="http://nextcloud.org/ns">
  <d:prop><d:getetag/></d:prop>
  <oc:is-favorite>1</oc:is-favorite>
</oc:filter-files>`

// A REPORT reaching a claiming source comes back as a multistatus of hits.
func TestAReportReachesItsSource(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.write(t, "fav.txt", "kept")
	m := favSourceWith(t, f, []core.Entry{f.entryAt(t, "fav.txt")})

	w := f.throughHeaders(m, "REPORT", "/dav/files/fav.txt", favReport, nil)

	if w.Code != http.StatusMultiStatus {
		t.Fatalf("answered %d, want 207: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "fav.txt") {
		t.Errorf("the hit is not in the answer: %s", w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "getetag") {
		t.Errorf("the requested property is not in the answer: %s", w.Body.String())
	}
}

// An empty DAV:prop defaults the response to an etag, which is what the
// clients ask for and what every entry has.
func TestAReportWithoutAPropDefaultsToTheETag(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.write(t, "fav.txt", "kept")
	body := strings.Replace(favReport, "<d:prop><d:getetag/></d:prop>", "", 1)
	m := favSourceWith(t, f, []core.Entry{f.entryAt(t, "fav.txt")})

	w := f.throughHeaders(m, "REPORT", "/dav/files/fav.txt", body, nil)

	if w.Code != http.StatusMultiStatus {
		t.Fatalf("answered %d, want 207", w.Code)
	}
	if !strings.Contains(w.Body.String(), "getetag") {
		t.Errorf("no default property: %s", w.Body.String())
	}
}

// A vocabulary no source claims is 501, not an empty result. The two mean
// different things, and an empty one teaches the client to show nothing.
func TestAnUnclaimedVocabularyIsNotImplemented(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.write(t, "a.txt", "contents")
	m := favSourceWith(t, f, nil)

	body := `<?xml version="1.0"?>
<v:unknown-report xmlns:v="http://nobody.example/ns"/>`
	w := f.throughHeaders(m, "REPORT", "/dav/files/a.txt", body, nil)

	if w.Code != http.StatusNotImplemented {
		t.Errorf("answered %d, want 501", w.Code)
	}
}

// A deployment with no sources refuses both methods rather than answering
// them as empty.
func TestSearchWithoutSourcesIsRefused(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.write(t, "a.txt", "contents")
	m := f.mounted()

	w := f.throughHeaders(m, "SEARCH", "/dav/files/a.txt", favReport, nil)
	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("SEARCH answered %d, want 405", w.Code)
	}
	w = f.throughHeaders(m, "REPORT", "/dav/files/a.txt", favReport, nil)
	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("REPORT answered %d, want 405", w.Code)
	}
}

// Allow advertises the query methods only when a source exists. A client
// reading a header that names SEARCH and sending one that answers 405 is the
// failure this stops.
func TestAllowMatchesWhatSearchCanRun(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.write(t, "a.txt", "contents")

	with := favSourceWith(t, f, nil)
	got := f.through(with, http.MethodOptions, "/dav/files/a.txt", "")
	if a := got.Header().Get("Allow"); !strings.Contains(a, "REPORT") {
		t.Errorf("a deployment with a source does not advertise REPORT: %q", a)
	}

	// A second deployment, built the plain way, has no source. Sharing the
	// first's fixture state is fine: the header is read per handler.
	plain := newFixture(t)
	plain.write(t, "a.txt", "contents")
	got = f.through(plain.mounted(), http.MethodOptions, "/dav/files/a.txt", "")
	if a := got.Header().Get("Allow"); strings.Contains(a, "REPORT") {
		t.Errorf("a deployment with no source advertises REPORT: %q", a)
	}
}

// A filter the source cannot apply is refused, not ignored. An ignored filter
// returns everything, which for a favourites view is the opposite of the view.
func TestAFilterTheSourceCannotApplyIsRefused(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.write(t, "a.txt", "contents")
	m := favSourceWith(t, f, []core.Entry{f.entryAt(t, "a.txt")})

	body := strings.Replace(favReport, "<oc:is-favorite>1</oc:is-favorite>",
		"<oc:is-favorite>blue</oc:is-favorite>", 1)
	w := f.throughHeaders(m, "REPORT", "/dav/files/a.txt", body, nil)

	if w.Code < http.StatusBadRequest {
		t.Errorf("an unusable filter answered %d, want a 4xx", w.Code)
	}
	if f.exists("a.txt") == false {
		t.Error("sanity: the fixture lost its file")
	}
}

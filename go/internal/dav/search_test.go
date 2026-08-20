//go:build linux

package dav

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/heavycaffeiner/stowcloud/go/internal/core"
)

// The query seam. What is proved here is that this package hands a vendor
// vocabulary through without interpreting it, and that a filter it cannot
// apply is refused rather than dropped.

const vendorNS = "http://owncloud.org/ns"

type fakeSource struct {
	// gotLeaves records what the parser handed over, which is the whole point
	// of the seam: the source sees the filter exactly as it was written.
	gotLeaves []Leaf
	gotWant   []Name
	hits      []core.Entry
	err       error
}

func (f *fakeSource) Namespaces() []string { return []string{vendorNS} }

func (f *fakeSource) Props(_ PropCtx, e core.Entry, want []Name) []Prop {
	var out []Prop
	for _, n := range want {
		if n.Space == vendorNS && n.Local == "favorite" {
			out = append(out, Prop{Name: n, Value: "1"})
		}
	}
	return out
}

func (f *fakeSource) Query(_ context.Context, _ core.Resolved, leaves []Leaf, want []Name) ([]core.Entry, error) {
	f.gotLeaves, f.gotWant = leaves, want
	return f.hits, f.err
}

func withSource(t *testing.T, src PropSource) *fixture {
	t.Helper()
	f := newFixture(t)
	f.h = New(Options{
		Core: f.core, State: f.store.State(),
		Locks: NewLocks(f.store.State(), f.clk), Sources: []PropSource{src},
	})
	return f
}

// The filter reaches the source with its namespace and value intact. This
// package never learns what "favorite" means.
func TestAReportHandsTheFilterToTheClaimingSourceVerbatim(t *testing.T) {
	src := &fakeSource{}
	f := withSource(t, src)
	f.write(t, "a.txt", "hello")

	body := `<oc:filter-files xmlns:oc="` + vendorNS + `" xmlns:D="DAV:">` +
		`<D:prop><D:getetag/><oc:fileid/></D:prop>` +
		`<oc:filter-rules><oc:favorite>1</oc:favorite></oc:filter-rules>` +
		`</oc:filter-files>`
	rec := f.do(t, "REPORT", "/", body, nil)
	if rec.Code != http.StatusMultiStatus {
		t.Fatalf("status = %d, want 207\n%s", rec.Code, rec.Body)
	}

	var fav *Leaf
	for i := range src.gotLeaves {
		if src.gotLeaves[i].Name.Local == "favorite" {
			fav = &src.gotLeaves[i]
		}
	}
	if fav == nil {
		t.Fatalf("the source never saw the filter: %+v", src.gotLeaves)
	}
	if fav.Name.Space != vendorNS || fav.Value != "1" {
		t.Fatalf("the filter reached the source altered: %+v", *fav)
	}
	if len(src.gotWant) != 2 {
		t.Fatalf("the requested property set was not passed through: %v", src.gotWant)
	}
}

// A source that cannot apply a term must fail the request. Silently dropping a
// filter returns more than the client asked for, which for a favourites view
// means the whole tree.
func TestASourceRefusingAFilterFailsTheRequest(t *testing.T) {
	src := &fakeSource{err: errors.New("dav: this source cannot apply that filter")}
	f := withSource(t, src)
	f.write(t, "a.txt", "hello")

	body := `<oc:filter-files xmlns:oc="` + vendorNS + `"><oc:mystery>x</oc:mystery></oc:filter-files>`
	rec := f.do(t, "REPORT", "/", body, nil)
	if rec.Code == http.StatusMultiStatus {
		t.Fatalf("a refused filter still produced a result document\n%s", rec.Body)
	}
}

// A vocabulary nobody claims cannot be answered. An empty multistatus would
// mean "nothing matched", which is a different and wrong answer.
func TestAnUnclaimedVocabularyIsRefusedRatherThanAnsweredEmpty(t *testing.T) {
	src := &fakeSource{}
	f := withSource(t, src)
	f.write(t, "a.txt", "hello")

	body := `<other:report xmlns:other="urn:nobody-claims-this"/>`
	rec := f.do(t, "REPORT", "/", body, nil)
	if rec.Code == http.StatusMultiStatus {
		t.Fatalf("an unclaimed report was answered with a result document\n%s", rec.Body)
	}
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

// Registering a source is what puts the methods into Allow.
func TestASourceEnablesSearchAndReport(t *testing.T) {
	src := &fakeSource{}
	f := withSource(t, src)
	f.write(t, "a.txt", "hello")

	allow := f.do(t, "OPTIONS", "/a.txt", "", nil).Header().Get("Allow")
	for _, m := range []string{"SEARCH", "REPORT"} {
		if !strings.Contains(allow, m) {
			t.Fatalf("Allow = %q, missing %s with a source registered", allow, m)
		}
	}
}

// A SEARCH body is XML from a stranger and gets the scanner's caps unchanged.
func TestASearchBodyGetsTheSameCapsAsAPropfind(t *testing.T) {
	src := &fakeSource{}
	f := withSource(t, src)
	f.write(t, "a.txt", "hello")

	body := `<!DOCTYPE x><oc:searchrequest xmlns:oc="` + vendorNS + `"/>`
	rec := f.do(t, "SEARCH", "/", body, nil)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400: a SEARCH body with a DTD", rec.Code)
	}
}

// A vendor property reaches a PROPFIND response through the source, without
// this package naming it.
func TestAVendorPropertyReachesAPropfindThroughTheSource(t *testing.T) {
	src := &fakeSource{}
	f := withSource(t, src)
	f.write(t, "a.txt", "hello")

	body := `<D:propfind xmlns:D="DAV:" xmlns:oc="` + vendorNS + `"><D:prop>` +
		`<oc:favorite/><D:getcontentlength/></D:prop></D:propfind>`
	rec := f.do(t, "PROPFIND", "/a.txt", body, http.Header{"Depth": {"0"}})
	doc := rec.Body.String()
	if !strings.Contains(doc, "favorite") {
		t.Fatalf("the vendor property never reached the response\n%s", doc)
	}
	if !strings.Contains(doc, "<D:getcontentlength>5</D:getcontentlength>") {
		t.Fatalf("the live property beside it was lost\n%s", doc)
	}
	// It was answered, so it must not also be reported missing. When there is
	// no 404 propstat at all the property cannot be in one.
	if i := strings.Index(doc, "404"); i >= 0 && strings.Contains(doc[i:], "favorite") {
		t.Fatalf("a property the source answered is also in the 404 set\n%s", doc)
	}
}

// A name in a namespace no source claims is a 404, not an error: the client
// asked for something this server does not have.
func TestAnUnclaimedPropertyIsA404NotAFailure(t *testing.T) {
	src := &fakeSource{}
	f := withSource(t, src)
	f.write(t, "a.txt", "hello")

	body := `<D:propfind xmlns:D="DAV:"><D:prop>` +
		`<mystery xmlns="urn:nobody"/></D:prop></D:propfind>`
	rec := f.do(t, "PROPFIND", "/a.txt", body, http.Header{"Depth": {"0"}})
	if rec.Code != http.StatusMultiStatus {
		t.Fatalf("status = %d, want 207", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "HTTP/1.1 404 Not Found") {
		t.Fatalf("an unclaimed property did not become a 404\n%s", rec.Body)
	}
}

package server

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/heavycaffeiner/stowcloud/go/internal/apierr"
)

// A Destination naming another origin is refused, not silently made local.
//
// The header carries an absolute URL as often as a path, and only the path was
// taken: COPY to https://elsewhere.example/dav/docs/x copied to /dav/docs/x on
// this server. A request that names somewhere else was answered as though it
// named here. RFC 4918 9.8.3 makes it a 502.
func TestAForeignDavDestinationIsRefused(t *testing.T) {
	r := httptest.NewRequest("COPY", "/dav/docs/src", nil)
	r.Host = "files.example"
	r.Header.Set("Destination", "https://elsewhere.example/dav/docs/dst")

	_, err := davDestination(nil, 0, r)
	if err == nil {
		t.Fatal("a destination on another origin was accepted")
	}
	var re *apierr.RequestError
	if !errors.As(err, &re) || re.Status != http.StatusBadGateway {
		t.Fatalf("refused with %v, want a 502", err)
	}
}

// The same host is this server, whatever the scheme. A reverse proxy
// terminates TLS and forwards http, so comparing schemes would refuse every
// move behind one.
func TestADavDestinationOnThisHostIsAccepted(t *testing.T) {
	// The predicate itself, because everything past it needs a resolver: what
	// is under test is which destinations count as this server.
	for _, dest := range []string{
		"https://files.example/dav/docs/dst",
		"http://files.example/dav/docs/dst",
		"https://FILES.EXAMPLE/dav/docs/dst",
		"/dav/docs/dst",
	} {
		u, perr := url.Parse(dest)
		if perr != nil {
			t.Fatalf("parsing %s: %v", dest, perr)
		}
		r := httptest.NewRequest("COPY", "/dav/docs/src", nil)
		r.Host = "files.example"
		if u.Host != "" && !sameOrigin(u, r) {
			t.Errorf("%s was read as another origin", dest)
		}
	}
}

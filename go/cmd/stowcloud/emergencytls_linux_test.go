//go:build linux

package main

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/heavycaffeiner/stowcloud/go/engine/http/emergency"
	"github.com/heavycaffeiner/stowcloud/go/engine/kit/clock"
	"github.com/heavycaffeiner/stowcloud/go/engine/kit/task"
)

// The door over TLS, with the standard library's own cookie jar.
//
// The other end-to-end tests run over plain HTTP and carry the session in a jar
// this file wrote, because the real cookie is __Host- prefixed and therefore
// Secure-only: net/http/cookiejar will not store it over http://, so those
// tests would have had to either drop the prefix or hand-roll the jar. They
// hand-rolled it, which is a substitute for the thing being tested.
//
// This is the thing being tested. A real TLS listener, the stdlib jar applying
// its own cookie rules, and no help from this file: if the door emits a cookie
// a browser would reject, the jar drops it and the authenticated routes answer
// 401. That is the failure a hand-written jar cannot produce.

// tlsListener starts the door behind a self-signed certificate for 127.0.0.1
// and returns the base URL and a client that trusts it.
func tlsListener(t *testing.T, h http.Handler) (string, *http.Client) {
	t.Helper()

	cert, pool := selfSigned(t)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	srv := &http.Server{
		Handler:           h,
		ReadHeaderTimeout: 30 * time.Second,
		TLSConfig:         &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12},
	}
	task.Go(t.Context(), "emergency tls test server", func() {
		if serr := srv.ServeTLS(ln, "", ""); serr != nil && serr != http.ErrServerClosed {
			t.Errorf("the TLS server stopped: %v", serr)
		}
	})
	t.Cleanup(func() {
		if cerr := srv.Close(); cerr != nil {
			t.Errorf("closing the TLS server: %v", cerr)
		}
	})

	// The standard jar, with its own rules about which cookies it will store
	// and return. Nothing in this file tells it what to do.
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatalf("cookie jar: %v", err)
	}
	client := &http.Client{
		Jar: jar,
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12},
		},
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	return "https://" + ln.Addr().String(), client
}

// ask sends a request and returns the status and body.
func ask(t *testing.T, c *http.Client, method, url, body string) (int, string) {
	t.Helper()
	var rdr io.Reader
	if body != "" {
		rdr = strings.NewReader(body)
	}
	req, err := http.NewRequestWithContext(t.Context(), method, url, rdr)
	if err != nil {
		t.Fatalf("building the request: %v", err)
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	res, err := c.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, url, err)
	}
	raw, rerr := io.ReadAll(res.Body)
	if cerr := res.Body.Close(); cerr != nil {
		t.Errorf("closing the response body: %v", cerr)
	}
	if rerr != nil {
		t.Fatalf("reading the response: %v", rerr)
	}
	return res.StatusCode, string(raw)
}

// The whole flow over TLS, with the session carried by the standard jar.
//
// This is what a browser does. The door sets a __Host- cookie, the jar decides
// whether that cookie is one it will keep, and the next request either carries
// it or does not. Nothing here reads the Set-Cookie header or reconstructs
// anything: the jar's answer is the assertion.
func TestTheDoorWorksOverTLSWithARealCookieJar(t *testing.T) {
	l := start(t)
	l.admin(t, "operator")

	base, client := tlsListener(t, emergency.Handler(emergency.Deps{
		Auth: l.svc, State: l.store, DataDir: l.dir,
		Restart: func() { l.restarts++ },
	}))

	// Before signing in, the settings are closed.
	if code, _ := ask(t, client, "GET", base+emergency.Prefix+"/api/settings", ""); code != http.StatusUnauthorized {
		t.Fatalf("the settings answered %d before a login", code)
	}

	code, body := ask(t, client, "POST", base+emergency.Prefix+"/api/login",
		`{"username":"operator","password":"`+testPassword+`"}`)
	if code != http.StatusOK {
		t.Fatalf("login returned %d: %s", code, body)
	}

	// The jar kept the cookie, which is only true if the door emitted one a
	// browser would accept: __Host- requires Secure, Path=/ and no Domain, and
	// the jar enforces all three.
	u, err := url.Parse(base)
	if err != nil {
		t.Fatal(err)
	}
	kept := client.Jar.Cookies(u)
	if len(kept) == 0 {
		t.Fatal("the standard jar stored no cookie, so the door emitted one a browser would reject")
	}
	var found bool
	for _, c := range kept {
		if c.Name == emergency.SessionCookie {
			found = true
		}
	}
	if !found {
		t.Errorf("the jar kept %v, none of them the session cookie", kept)
	}

	// And the session works, carried entirely by the jar.
	if sc, sb := ask(t, client, "GET", base+emergency.Prefix+"/api/settings", ""); sc != http.StatusOK {
		t.Fatalf("the settings answered %d with a jar-carried session: %s", sc, sb)
	}

	// A write, through the same session.
	code, body = ask(t, client, "PATCH", base+emergency.Prefix+"/api/settings/search",
		`{"max_concurrent_fast":6}`)
	if code != http.StatusOK {
		t.Fatalf("the write answered %d: %s", code, body)
	}

	// Read back from the database rather than from the response.
	doc, derr := l.store.Settings(t.Context())
	if derr != nil {
		t.Fatal(derr)
	}
	if got := number(t, section(t, doc, "search"), "max_concurrent_fast"); got != 6 {
		t.Errorf("the stored value is %v, want 6", got)
	}
}

// What the standard jar does and does not enforce, measured rather than
// assumed, because the test above is only worth what the jar checks.
//
// Go's cookiejar implements RFC 6265 and the __Host- prefix is a later
// convention (the prefixes draft, which browsers adopted and 6265 predates), so
// the jar enforces one third of it:
//
//	conforming      kept
//	without Secure  kept    <- a browser rejects this
//	Path=/sub       dropped <- the one rule the jar does apply
//	with Domain     kept    <- a browser rejects this
//
// So the TLS test above proves the cookie survives a real jar over real TLS and
// carries a session, which is what a hand-written jar could not show. It does
// not prove the cookie meets the __Host- rules: the jar does not check two of
// the three. That check stays where it can be made, on the emitted Set-Cookie
// header, in the package's own test.
//
// This records the measurement so nobody later reads the TLS test as covering
// more than it does.
func TestTheStandardJarEnforcesOnlyThePathRule(t *testing.T) {
	u, err := url.Parse("https://127.0.0.1:8443/")
	if err != nil {
		t.Fatal(err)
	}
	// Building a cookie the browser rules forbid is the whole measurement, so
	// the linter's objection is answered once here rather than at each case.
	probe := func(path string, secure bool, domain string) *http.Cookie {
		return &http.Cookie{ //nolint:gosec // G124: a deliberately non-conforming cookie, which is what this measures.
			Name: emergency.SessionCookie, Value: "abcd",
			Path: path, Secure: secure, Domain: domain,
			HttpOnly: true, SameSite: http.SameSiteLaxMode,
		}
	}
	kept := func(c *http.Cookie) int {
		j, jerr := cookiejar.New(nil)
		if jerr != nil {
			t.Fatal(jerr)
		}
		j.SetCookies(u, []*http.Cookie{c})
		return len(j.Cookies(u))
	}

	// The cookie the door emits is one the jar keeps, which is what makes the
	// TLS test above able to authenticate at all.
	if got := kept(probe("/", true, "")); got != 1 {
		t.Fatalf("the jar rejected the cookie the door actually emits (kept %d)", got)
	}

	// The path rule is enforced, so the jar is doing something rather than
	// accepting everything.
	if got := kept(probe("/sub", true, "")); got != 0 {
		t.Errorf("the jar kept a __Host- cookie scoped to a subpath (kept %d)", got)
	}

	// And these are the two it does not enforce. Asserted so the gap is a
	// recorded measurement rather than an assumption, and so a future Go
	// release that starts enforcing them shows up here as a failure to read
	// rather than as a silent change.
	if got := kept(probe("/", false, "")); got != 1 {
		t.Logf("this Go release now rejects a __Host- cookie without Secure (kept %d); "+
			"the TLS test above covers more than it used to", got)
	}
	if got := kept(probe("/", true, "127.0.0.1")); got != 1 {
		t.Logf("this Go release now rejects a __Host- cookie naming a Domain (kept %d)", got)
	}
}

// selfSigned makes a certificate for 127.0.0.1 and a pool that trusts it.
//
// Written here rather than borrowed from the server's own generator, which is
// unexported: widening a production surface so a test can reach it is a worse
// trade than twenty lines of fixture.
func selfSigned(t *testing.T) (tls.Certificate, *x509.CertPool) {
	t.Helper()

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generating a key: %v", err)
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		t.Fatalf("generating a serial: %v", err)
	}
	// Through the clock kit, which is the only place that reads a wall clock:
	// a certificate's validity window is a timestamp like any other.
	now := clock.System().Now()
	tmpl := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: "127.0.0.1"},
		NotBefore:             now.Add(-time.Hour),
		NotAfter:              now.Add(time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		IPAddresses:           []net.IP{net.ParseIP("127.0.0.1")},
		IsCA:                  true,
		BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("creating the certificate: %v", err)
	}
	leaf, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatalf("parsing the certificate: %v", err)
	}
	pool := x509.NewCertPool()
	pool.AddCert(leaf)
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key, Leaf: leaf}, pool
}

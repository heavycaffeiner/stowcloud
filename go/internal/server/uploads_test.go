// Linux only, because what it tests is.
//go:build linux

package server

import (
	"context"
	"encoding/base64"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/heavycaffeiner/stowcloud/go/internal/acl"
	"github.com/heavycaffeiner/stowcloud/go/internal/clock"
	"github.com/heavycaffeiner/stowcloud/go/internal/core"
	"github.com/heavycaffeiner/stowcloud/go/internal/httpapi"
	"github.com/heavycaffeiner/stowcloud/go/internal/httpapi/handler"
	"github.com/heavycaffeiner/stowcloud/go/internal/httpapi/mw"
	"github.com/heavycaffeiner/stowcloud/go/internal/httpapi/route"
	"github.com/heavycaffeiner/stowcloud/go/internal/store"
	"github.com/heavycaffeiner/stowcloud/go/internal/upload"
	"github.com/heavycaffeiner/stowcloud/go/internal/vfs"
)

// The resumable upload surface, through the real chain.
//
// The engine's own contracts are Phase 6's and are proved there. What is
// proved here is the mapping from protocol to engine: that a client speaking
// this protocol can create a session, send chunks, learn where to resume from,
// and end up with the bytes it sent.

type uploadFixture struct {
	handler http.Handler
	cookie  *http.Cookie
	csrf    string
	host    string
}

func newUploadFixture(t *testing.T) *uploadFixture {
	t.Helper()
	clk := clock.Fixed(time.Unix(0, 1_700_000_000_000_000_000))
	dir := t.TempDir()
	st, err := store.Open(dir, store.Options{Clock: clk})
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() {
		if cerr := st.Close(); cerr != nil {
			t.Errorf("closing the store: %v", cerr)
		}
	})

	svc := newTestAuth(t, dir, st, clk)
	ctx := context.Background()
	if _, uerr := svc.CreateUser(ctx, "alice", "Alice", pwSecret(t, "correct-horse")); uerr != nil {
		t.Fatalf("CreateUser: %v", uerr)
	}
	sess, err := svc.CreateSession(ctx, 1, "127.0.0.1", "test", 1, 0)
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	ev := acl.NewEvaluator()
	coreSvc, err := core.New(st, core.Options{ACL: ev, Clock: clk})
	if err != nil {
		t.Fatalf("core.New: %v", err)
	}
	host := t.TempDir()
	if mderr := os.MkdirAll(host+"/docs", 0o775); mderr != nil {
		t.Fatalf("mkdir: %v", mderr)
	}
	if rerr := coreSvc.RegisterShare(ctx, core.ShareDef{
		ID: 1, Name: "docs", Host: host + "/docs", Policy: vfs.DefaultSharePolicy(),
	}); rerr != nil {
		t.Fatalf("RegisterShare: %v", rerr)
	}
	g := acl.Grant{
		User: 1, Share: 1, Subpath: acl.NewPath(),
		Allow:   acl.Read | acl.Create | acl.Write | acl.Delete,
		Inherit: true, Label: "docs",
	}
	if gerr := insertGrant(st, g, 1); gerr != nil {
		t.Fatalf("insertGrant: %v", gerr)
	}
	if lerr := ev.LoadFromState(ctx, st.State().SQL()); lerr != nil {
		t.Fatalf("reloading grants: %v", lerr)
	}

	engine, err := upload.New(ctx, coreSvc, st.State(), upload.Options{Clock: clk})
	if err != nil {
		t.Fatalf("upload.New: %v", err)
	}

	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	state := &httpapi.State{
		Log: log, Clock: clk, Auth: svc, Core: coreSvc,
		Trusted: mw.NewTrustedSet([]netip.Prefix{mustPrefix("127.0.0.0/8")}),
		Hosts:   mw.NewHostSet([]string{"localhost"}, nil),
		CSRFKey: make([]byte, 32),
		Limiter: mw.NewRateLimiter(1000, 10000, clk),
	}
	deps := handler.Deps{
		Core: coreSvc, Auth: svc, Clock: clk, Log: log,
		Limiter: state.Limiter, Trusted: state.Trusted, Hosts: state.Hosts,
		CSRFKey: make([]byte, 32), WatchCap: func() int { return 4096 },
		Uploads: engine,
	}
	gate, err := NewSetupGate(ctx, svc, clk, dir)
	if err != nil {
		t.Fatalf("NewSetupGate: %v", err)
	}
	table := routes(deps, gate)
	if err := route.Validate(table); err != nil {
		t.Fatalf("route.Validate: %v", err)
	}
	state.SetLookup(route.From(table))

	token := sessionTokenHex(sess)
	return &uploadFixture{
		handler: httpapi.Chain(state)(mux(table, nil, nil)),
		cookie: &http.Cookie{
			Name: mw.SessionCookie, Value: token, Path: "/",
			Secure: true, HttpOnly: true, SameSite: http.SameSiteLaxMode,
		},
		csrf: mw.DeriveCSRFToken(make([]byte, 32), token),
		host: host + "/docs",
	}
}

func (f *uploadFixture) do(t *testing.T, method, path string, body io.Reader, headers map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, body)
	req.Host = "localhost"
	req.AddCookie(f.cookie)
	// What a browser sends on a state-changing request, and what the chain
	// requires: the origin it came from and the token derived from the
	// session. A test that skipped them would be testing the CSRF step.
	req.Header.Set("Origin", "https://localhost")
	req.Header.Set("Sc-Csrf", f.csrf)
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	f.handler.ServeHTTP(rec, req)
	return rec
}

// metadata builds the header the destination is carried in, since the protocol
// has nowhere else to put it.
func metadata(pairs map[string]string) string {
	out := make([]string, 0, len(pairs))
	for k, v := range pairs {
		out = append(out, k+" "+base64.StdEncoding.EncodeToString([]byte(v)))
	}
	return strings.Join(out, ",")
}

// A client creates a session, sends the file in two chunks, and the bytes on
// disk are the bytes it sent.
func TestAnUploadRoundTrips(t *testing.T) {
	f := newUploadFixture(t)
	content := strings.Repeat("A", 6<<20) + strings.Repeat("B", 1<<20)

	created := f.do(t, "POST", "/api/uploads", nil, map[string]string{
		"Tus-Resumable":   "1.0.0",
		"Upload-Length":   strconv.Itoa(len(content)),
		"Upload-Metadata": metadata(map[string]string{"dest": "docs", "filename": "big.bin"}),
	})
	if created.Code != http.StatusCreated {
		t.Fatalf("create = %d, want 201\n%s", created.Code, created.Body)
	}
	location := created.Header().Get("Location")
	if location == "" {
		t.Fatal("the creation named no session")
	}
	if got := created.Header().Get("Upload-Offset"); got != "0" {
		t.Fatalf("a fresh session starts at %q, want 0", got)
	}

	// The first chunk.
	first := content[:6<<20]
	patch := f.do(t, "PATCH", location, strings.NewReader(first), map[string]string{
		"Tus-Resumable": "1.0.0",
		"Upload-Offset": "0",
		"Content-Type":  "application/offset+octet-stream",
	})
	if patch.Code != http.StatusNoContent {
		t.Fatalf("first chunk = %d, want 204\n%s", patch.Code, patch.Body)
	}
	if got := patch.Header().Get("Upload-Offset"); got != strconv.Itoa(len(first)) {
		t.Fatalf("offset after the first chunk = %q, want %d", got, len(first))
	}

	// A resume asks where to continue from, which is the whole point of the
	// protocol: a client that lost its connection must not guess.
	head := f.do(t, "HEAD", location, nil, map[string]string{"Tus-Resumable": "1.0.0"})
	if head.Code != http.StatusNoContent {
		t.Fatalf("head = %d, want 204", head.Code)
	}
	if got := head.Header().Get("Upload-Offset"); got != strconv.Itoa(len(first)) {
		t.Fatalf("resume offset = %q, want %d", got, len(first))
	}
	if got := head.Header().Get("Upload-Length"); got != strconv.Itoa(len(content)) {
		t.Fatalf("declared length = %q, want %d", got, len(content))
	}
	// The session fixes its chunk size at creation, so a configuration change
	// afterwards cannot break a session in flight, and a resuming client
	// follows this rather than a value it remembered.
	if head.Header().Get("Sc-Chunk-Size") == "" {
		t.Error("the resume carries no chunk size")
	}
	// A remembered offset is a client writing over bytes it already sent.
	if cc := head.Header().Get("Cache-Control"); !strings.Contains(cc, "no-store") {
		t.Errorf("Cache-Control = %q, want no-store", cc)
	}

	// The rest.
	rest := content[len(first):]
	done := f.do(t, "PATCH", location, strings.NewReader(rest), map[string]string{
		"Tus-Resumable": "1.0.0",
		"Upload-Offset": strconv.Itoa(len(first)),
		"Content-Type":  "application/offset+octet-stream",
	})
	if done.Code != http.StatusNoContent {
		t.Fatalf("final chunk = %d, want 204\n%s", done.Code, done.Body)
	}
	if got := done.Header().Get("Upload-Offset"); got != strconv.Itoa(len(content)) {
		t.Fatalf("final offset = %q, want %d", got, len(content))
	}

	// The part that makes this a round trip rather than a set of accepted
	// chunks. Every assertion above passed while the bytes stopped at the part
	// file: the session was never finalized, so the destination never existed
	// and the listing stayed empty, on every deployment.
	listed := f.do(t, "GET", "/api/fs/list?path=/docs", nil, nil)
	if listed.Code != http.StatusOK {
		t.Fatalf("list = %d, want 200\n%s", listed.Code, listed.Body)
	}
	if !strings.Contains(listed.Body.String(), "big.bin") {
		t.Fatalf("the uploaded file is not in the listing:\n%s", listed.Body)
	}

	// Checked through stat rather than by reading the bytes back: this
	// fixture's grant deliberately withholds Download, and widening it to make
	// an assertion convenient would stop the other tests here from meaning
	// what they say.
	stated := f.do(t, "GET", "/api/fs/stat?path=/docs/big.bin", nil, nil)
	if stated.Code != http.StatusOK {
		t.Fatalf("stat = %d, want 200\n%s", stated.Code, stated.Body)
	}
	if !strings.Contains(stated.Body.String(), strconv.Itoa(len(content))) {
		t.Errorf("the published file is not %d bytes:\n%s", len(content), stated.Body)
	}

	// The session is consumed by the publish, so a client that asks again is
	// told it is gone rather than being handed a resumable offset for a file
	// that is already on disk.
	gone := f.do(t, "HEAD", location, nil, map[string]string{"Tus-Resumable": "1.0.0"})
	if gone.Code != http.StatusNotFound {
		t.Errorf("head after publish = %d, want 404", gone.Code)
	}
}

// A chunk that did not arrive at the resumable offset has its own status, so a
// client reads it as "resume from what the response says" rather than as a
// destination that already exists.
func TestAChunkAtTheWrongOffsetIsRefused(t *testing.T) {
	f := newUploadFixture(t)
	created := f.do(t, "POST", "/api/uploads", nil, map[string]string{
		"Tus-Resumable":   "1.0.0",
		"Upload-Length":   "1024",
		"Upload-Metadata": metadata(map[string]string{"dest": "docs", "filename": "a.bin"}),
	})
	if created.Code != http.StatusCreated {
		t.Fatalf("create = %d\n%s", created.Code, created.Body)
	}

	rec := f.do(t, "PATCH", created.Header().Get("Location"), strings.NewReader("hello"), map[string]string{
		"Tus-Resumable": "1.0.0",
		"Upload-Offset": "512",
		"Content-Type":  "application/offset+octet-stream",
	})
	if rec.Code != http.StatusConflict {
		t.Fatalf("a chunk at the wrong offset = %d, want 409\n%s", rec.Code, rec.Body)
	}
}

// The destination is carried in the metadata header, and neither half alone
// names a target: the directory is not a file, and the leaf has no share label.
func TestACreationWithNoDestinationIsRefused(t *testing.T) {
	f := newUploadFixture(t)
	for name, meta := range map[string]string{
		"nothing at all":     "",
		"a directory only":   metadata(map[string]string{"dest": "docs"}),
		"an empty file name": metadata(map[string]string{"dest": "docs", "filename": ""}),
	} {
		rec := f.do(t, "POST", "/api/uploads", nil, map[string]string{
			"Tus-Resumable":   "1.0.0",
			"Upload-Length":   "10",
			"Upload-Metadata": meta,
		})
		if rec.Code != http.StatusBadRequest {
			t.Errorf("%s = %d, want 400\n%s", name, rec.Code, rec.Body)
		}
	}
}

// A session belonging to somebody else and one that never existed answer
// identically, so a stranger cannot learn which ids exist.
func TestAnUnknownSessionIsIndistinguishable(t *testing.T) {
	f := newUploadFixture(t)

	// A well-formed id that was never minted.
	minted := f.do(t, "POST", "/api/uploads", nil, map[string]string{
		"Tus-Resumable":   "1.0.0",
		"Upload-Length":   "10",
		"Upload-Metadata": metadata(map[string]string{"dest": "docs", "filename": "a.bin"}),
	})
	if minted.Code != http.StatusCreated {
		t.Fatalf("create = %d", minted.Code)
	}
	real := minted.Header().Get("Location")
	id := real[strings.LastIndexByte(real, '/')+1:]

	// Same length and alphabet, different value.
	other := strings.Repeat("A", len(id))
	unknown := f.do(t, "HEAD", "/api/uploads/"+other, nil, map[string]string{"Tus-Resumable": "1.0.0"})
	malformed := f.do(t, "HEAD", "/api/uploads/not-a-real-id", nil, map[string]string{"Tus-Resumable": "1.0.0"})

	if unknown.Code != http.StatusNotFound || malformed.Code != http.StatusNotFound {
		t.Fatalf("unknown = %d, malformed = %d, want both 404", unknown.Code, malformed.Code)
	}
}

// The discovery request is how a client learns which version to speak, so it
// needs no credential and says nothing about the deployment.
func TestTheDiscoveryRequestNeedsNoCredential(t *testing.T) {
	f := newUploadFixture(t)
	req := httptest.NewRequest("OPTIONS", "/api/uploads", nil)
	req.Host = "localhost"
	rec := httptest.NewRecorder()
	f.handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("discovery = %d, want 204", rec.Code)
	}
	if got := rec.Header().Get("Tus-Version"); got != "1.0.0" {
		t.Errorf("Tus-Version = %q", got)
	}
	// An extension advertised and then ignored turns a real guarantee into a
	// believed one, so what is listed here has to be implemented.
	ext := rec.Header().Get("Tus-Extension")
	for _, want := range []string{"creation", "creation-with-upload", "checksum", "termination"} {
		if !strings.Contains(ext, want) {
			t.Errorf("Tus-Extension = %q, missing %s", ext, want)
		}
	}
}

// A digest that does not match refuses the chunk rather than accepting it. An
// extension that is advertised and not compared is worse than one never
// claimed: the client's integrity check passes without anything being checked.
func TestAMismatchedChecksumIsRefused(t *testing.T) {
	f := newUploadFixture(t)
	created := f.do(t, "POST", "/api/uploads", nil, map[string]string{
		"Tus-Resumable":   "1.0.0",
		"Upload-Length":   "5",
		"Upload-Metadata": metadata(map[string]string{"dest": "docs", "filename": "sum.bin"}),
	})
	if created.Code != http.StatusCreated {
		t.Fatalf("create = %d\n%s", created.Code, created.Body)
	}
	location := created.Header().Get("Location")

	// A digest of the wrong length and value for the body being sent.
	wrong := base64.StdEncoding.EncodeToString([]byte("0123456789abcdef0123456789abcdef"))
	rec := f.do(t, "PATCH", location, strings.NewReader("hello"), map[string]string{
		"Tus-Resumable":   "1.0.0",
		"Upload-Offset":   "0",
		"Content-Type":    "application/offset+octet-stream",
		"Upload-Checksum": "blake3 " + wrong,
	})
	if rec.Code == http.StatusNoContent {
		t.Fatal("a chunk with a wrong digest was accepted")
	}

	// And a header naming an algorithm this build cannot verify is refused
	// rather than ignored.
	bad := f.do(t, "PATCH", location, strings.NewReader("hello"), map[string]string{
		"Tus-Resumable":   "1.0.0",
		"Upload-Offset":   "0",
		"Content-Type":    "application/offset+octet-stream",
		"Upload-Checksum": "md5 " + wrong,
	})
	if bad.Code != http.StatusBadRequest {
		t.Fatalf("an unverifiable algorithm = %d, want 400\n%s", bad.Code, bad.Body)
	}
}

// The upload mount is outside the body-size limit. A chunk is bounded by the
// session's declared length, not by a per-request ceiling that would refuse
// exactly the requests this surface exists for.
func TestAChunkIsNotRefusedByTheRequestBodyLimit(t *testing.T) {
	f := newUploadFixture(t)
	// Comfortably past the general request-body ceiling.
	body := strings.Repeat("x", 6<<20)

	created := f.do(t, "POST", "/api/uploads", nil, map[string]string{
		"Tus-Resumable":   "1.0.0",
		"Upload-Length":   strconv.Itoa(len(body)),
		"Upload-Metadata": metadata(map[string]string{"dest": "docs", "filename": "large.bin"}),
	})
	if created.Code != http.StatusCreated {
		t.Fatalf("create = %d\n%s", created.Code, created.Body)
	}

	rec := f.do(t, "PATCH", created.Header().Get("Location"), strings.NewReader(body), map[string]string{
		"Tus-Resumable": "1.0.0",
		"Upload-Offset": "0",
		"Content-Type":  "application/offset+octet-stream",
	})
	if rec.Code != http.StatusNoContent {
		t.Fatalf("a chunk past the general body limit = %d, want 204\n%s", rec.Code, rec.Body)
	}
	if got := rec.Header().Get("Upload-Offset"); got != strconv.Itoa(len(body)) {
		t.Fatalf("offset = %q, want the whole chunk at %d", got, len(body))
	}
}

// A version this build does not speak is refused, rather than being treated as
// the one it does.
func TestAnUnknownProtocolVersionIsRefused(t *testing.T) {
	f := newUploadFixture(t)
	rec := f.do(t, "POST", "/api/uploads", nil, map[string]string{
		"Tus-Resumable":   "2.0.0",
		"Upload-Length":   "10",
		"Upload-Metadata": metadata(map[string]string{"dest": "docs", "filename": "a.bin"}),
	})
	if rec.Code != http.StatusPreconditionFailed {
		t.Fatalf("an unknown version = %d, want 412\n%s", rec.Code, rec.Body)
	}
}

// Abandoning a session releases it, and the id stops working.
func TestATerminatedSessionIsGone(t *testing.T) {
	f := newUploadFixture(t)
	created := f.do(t, "POST", "/api/uploads", nil, map[string]string{
		"Tus-Resumable":   "1.0.0",
		"Upload-Length":   "10",
		"Upload-Metadata": metadata(map[string]string{"dest": "docs", "filename": "gone.bin"}),
	})
	if created.Code != http.StatusCreated {
		t.Fatalf("create = %d", created.Code)
	}
	location := created.Header().Get("Location")

	if rec := f.do(t, "DELETE", location, nil, map[string]string{"Tus-Resumable": "1.0.0"}); rec.Code != http.StatusNoContent {
		t.Fatalf("terminate = %d, want 204\n%s", rec.Code, rec.Body)
	}
	if rec := f.do(t, "HEAD", location, nil, map[string]string{"Tus-Resumable": "1.0.0"}); rec.Code != http.StatusNotFound {
		t.Fatalf("a terminated session answers %d, want 404", rec.Code)
	}
}

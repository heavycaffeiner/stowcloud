//go:build linux

package lifecycle_test

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/heavycaffeiner/stowcloud/go/engine/lifecycle"
	"github.com/heavycaffeiner/stowcloud/go/engine/service/core"
)

// sseEvent is one frame read off the stream.
type sseEvent struct {
	name string
	data string
}

// readSSE performs a request and collects the events, so a test reads frames
// rather than a blob of text.
func readSSE(t *testing.T, url string, sess session) (int, http.Header, []sseEvent) {
	t.Helper()

	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		t.Fatalf("building: %v", err)
	}
	sess.attach(req)

	resp, err := testClient().Do(req)
	if err != nil {
		t.Fatalf("requesting: %v", err)
	}
	defer func() {
		if cerr := resp.Body.Close(); cerr != nil {
			t.Errorf("closing: %v", cerr)
		}
	}()

	var events []sseEvent
	var pending string
	scanner := bufio.NewScanner(resp.Body)
	for scanner.Scan() {
		line := scanner.Text()
		switch {
		case strings.HasPrefix(line, "event: "):
			pending = strings.TrimPrefix(line, "event: ")
		case strings.HasPrefix(line, "data: "):
			events = append(events, sseEvent{name: pending, data: strings.TrimPrefix(line, "data: ")})
			pending = ""
		}
	}
	return resp.StatusCode, resp.Header, events
}

// A search finds a file and ends with a terminal event.
func TestSearchingFindsAFile(t *testing.T) {
	base, sess, share := contentShare(t, everyPerm(), []byte("unused"))

	for _, name := range []string{"report-january.txt", "report-february.txt", "unrelated.bin"} {
		if status, body := upload(t, base, sess, "/"+share+"/"+name, []byte("x")); status != http.StatusOK {
			t.Fatalf("writing %s answered %d: %s", name, status, body)
		}
	}

	status, header, events := readSSE(t, base+"/api/v1/search/stream?q=report", sess)
	if status != http.StatusOK {
		t.Fatalf("answered %d", status)
	}

	// The protocol's headers. Without the buffering hint an intermediary can
	// hold the whole stream and deliver it at the end, which is the one thing
	// streaming exists to avoid.
	if ct := header.Get("Content-Type"); !strings.HasPrefix(ct, "text/event-stream") {
		t.Errorf("the stream is typed %q", ct)
	}
	if cc := header.Get("Cache-Control"); !strings.Contains(cc, "no-store") {
		t.Errorf("Cache-Control is %q, so a search result can be cached", cc)
	}
	if header.Get("X-Accel-Buffering") != "no" {
		t.Error("no buffering hint, so a proxy may deliver the stream only at the end")
	}

	var hits []string
	var done int
	for _, e := range events {
		switch e.name {
		case "hit":
			var hit map[string]any
			if err := json.Unmarshal([]byte(e.data), &hit); err != nil {
				t.Fatalf("decoding a hit %q: %v", e.data, err)
			}
			hits = append(hits, stringField(hit, "name"))
		case "done":
			done++
		}
	}

	if done != 1 {
		t.Errorf("%d terminal events, want exactly 1", done)
	}
	if len(hits) != 2 {
		t.Errorf("the query matched %v, want the two reports", hits)
	}
	for _, name := range hits {
		if !strings.Contains(name, "report") {
			t.Errorf("%q does not match the query", name)
		}
	}
}

// A hit names the path the caller asked about.
//
// A share-relative fragment would make the client reassemble the path, and a
// client that got it wrong would open the wrong file.
func TestASearchHitCarriesTheNavigablePath(t *testing.T) {
	base, sess, share := contentShare(t, everyPerm(), []byte("unused"))

	if status, _ := upload(t, base, sess, "/"+share+"/sub/findme.txt", []byte("x")); status != http.StatusOK {
		t.Fatal("writing failed")
	}

	_, _, events := readSSE(t, base+"/api/v1/search/stream?q=findme", sess)

	var found bool
	for _, e := range events {
		if e.name != "hit" {
			continue
		}
		var hit map[string]any
		if err := json.Unmarshal([]byte(e.data), &hit); err != nil {
			t.Fatal(err)
		}
		path := stringField(hit, "path")
		if !strings.Contains(path, "findme.txt") {
			continue
		}
		found = true

		// The exact path, not a prefix. A prefix check passed on
		// "/docssub/findme.txt", where the share label and the first path
		// segment had run together: the client would ask for a share that
		// does not exist.
		want := "/" + share + "/sub/findme.txt"
		if path != want {
			t.Errorf("the hit path is %q, want %q", path, want)
		}

		// And it is a path this server will actually serve, which is the
		// claim the field makes.
		code, _, body := download(t, base, sess, path, "")
		if code != http.StatusOK {
			t.Errorf("the hit path %q does not read back: %d %s", path, code, body)
		}
	}
	if !found {
		t.Error("the file was not found")
	}
}

// A search only reports what the caller may read.
//
// The filter runs per entry rather than per share, because a grant can begin
// partway down a tree: a share-level answer would either conceal a readable
// subtree or list an unreadable one.
//
// Both accounts live on one engine. A second account's session proves the
// filter runs on the entry, not on whether the caller is signed in at all.
func TestSearchOnlyReportsReadableFiles(t *testing.T) {
	ctx := context.Background()

	e, err := lifecycle.Open(ctx, lifecycle.Options{DataDir: t.TempDir()})
	if err != nil {
		t.Fatalf("opening: %v", err)
	}
	t.Cleanup(func() {
		if cerr := e.Close(); cerr != nil {
			t.Errorf("closing: %v", cerr)
		}
	})

	if _, cerr := e.Auth.CreateUser(ctx, "alice", "Alice", pwOf(loginPassword)); cerr != nil {
		t.Fatal(cerr)
	}
	if _, cerr := e.Auth.CreateUser(ctx, "bob", "Bob", pwOf(loginPassword)); cerr != nil {
		t.Fatal(cerr)
	}

	host := t.TempDir()
	if werr := os.WriteFile(filepath.Join(host, "secret-doc.txt"), []byte("x"), 0o600); werr != nil {
		t.Fatal(werr)
	}
	sh, err := e.Core.CreateShare(ctx, core.ShareSpec{Name: "vault", Host: host})
	if err != nil {
		t.Fatal(err)
	}

	// Only the first account is granted anything.
	granted, err := e.Auth.UserIDByName(ctx, "alice")
	if err != nil {
		t.Fatal(err)
	}
	if _, gerr := e.Core.CreateGrant(ctx, core.GrantSpec{
		User: &granted, Share: sh.ID, Allow: everyPerm(), Inherit: true, Label: sh.Name,
	}); gerr != nil {
		t.Fatal(gerr)
	}
	if rerr := e.Core.ReloadGrants(ctx); rerr != nil {
		t.Fatal(rerr)
	}

	base := serve(t, e)
	grantedSess := signIn(t, base, "alice", loginPassword)
	deniedSess := signIn(t, base, "bob", loginPassword)

	// The granted account finds it, which is what makes the denial below
	// meaningful rather than a search that finds nothing for anybody.
	_, _, grantedEvents := readSSE(t, base+"/api/v1/search/stream?q=secret", grantedSess)
	var grantedHits int
	for _, ev := range grantedEvents {
		if ev.name == "hit" {
			grantedHits++
		}
	}
	if grantedHits == 0 {
		t.Fatal("the granted account found nothing, so this test cannot show a denial")
	}

	_, _, deniedEvents := readSSE(t, base+"/api/v1/search/stream?q=secret", deniedSess)
	for _, ev := range deniedEvents {
		if ev.name == "hit" {
			t.Errorf("an account with no grant received a hit: %s", ev.data)
		}
	}
}

// An empty query is refused rather than walking every share.
//
// A query that matched everything is a way to make one request cost a full
// scan of the deployment.
func TestAnEmptySearchQueryIsRefused(t *testing.T) {
	base, sess, _ := contentShare(t, everyPerm(), []byte("unused"))

	for _, q := range []string{"", "   ", "%20"} {
		status, _ := authed(t, http.MethodGet, base+"/api/v1/search/stream?q="+q, sess)
		if status == http.StatusOK {
			t.Errorf("the query %q was accepted", q)
		}
	}
}

// An oversized query is refused before it reaches the matcher.
func TestAnOversizedSearchQueryIsRefused(t *testing.T) {
	base, sess, _ := contentShare(t, everyPerm(), []byte("unused"))

	status, _ := authed(t, http.MethodGet,
		base+"/api/v1/search/stream?q="+strings.Repeat("a", 600), sess)
	if status == http.StatusOK {
		t.Error("a 600-character query was accepted")
	}
}

// A search needs a credential.
func TestSearchNeedsACredential(t *testing.T) {
	base, _, _ := contentShare(t, everyPerm(), []byte("unused"))

	// The refusal is disguised as a missing address: middleware.scopeHandler
	// answers every refusal with 404 rather than 401 or 403, so a stranger
	// probing the surface cannot map which routes exist.
	status, body := get(t, base+"/api/v1/search/stream?q=anything")
	if status != http.StatusNotFound {
		t.Errorf("an unauthenticated search answered %d: %s", status, body)
	}
}

// A query matching nothing still ends properly.
//
// A client waits for the terminal event. Without one it waits for the
// connection to close, which looks like a search that never finished.
func TestASearchMatchingNothingStillEnds(t *testing.T) {
	base, sess, _ := contentShare(t, everyPerm(), []byte("unused"))

	_, _, events := readSSE(t, base+"/api/v1/search/stream?q=zzzznothingmatches", sess)

	var done int
	for _, e := range events {
		if e.name == "hit" {
			t.Errorf("an unmatched query produced a hit: %s", e.data)
		}
		if e.name == "done" {
			done++
		}
	}
	if done != 1 {
		t.Errorf("%d terminal events for an empty result, want 1", done)
	}
}

// A grant that begins partway down a share hides what is above it.
//
// This is the case the share label cannot cover: the account can see the
// share, so the label resolves and the source survives. Only the per-entry
// check keeps a file outside the granted subtree out of the results.
func TestSearchRespectsAGrantThatStartsPartwayDown(t *testing.T) {
	ctx := context.Background()

	e, err := lifecycle.Open(ctx, lifecycle.Options{DataDir: t.TempDir()})
	if err != nil {
		t.Fatalf("opening: %v", err)
	}
	t.Cleanup(func() {
		if cerr := e.Close(); cerr != nil {
			t.Errorf("closing: %v", cerr)
		}
	})

	if _, cerr := e.Auth.CreateUser(ctx, "alice", "Alice", pwOf(loginPassword)); cerr != nil {
		t.Fatal(cerr)
	}

	// One file inside the granted subtree and one above it, both matching.
	host := t.TempDir()
	if merr := os.MkdirAll(filepath.Join(host, "allowed"), 0o700); merr != nil {
		t.Fatal(merr)
	}
	if werr := os.WriteFile(filepath.Join(host, "allowed", "target-inside.txt"), []byte("x"), 0o600); werr != nil {
		t.Fatal(werr)
	}
	if werr := os.WriteFile(filepath.Join(host, "target-outside.txt"), []byte("x"), 0o600); werr != nil {
		t.Fatal(werr)
	}

	sh, err := e.Core.CreateShare(ctx, core.ShareSpec{Name: "vault", Host: host})
	if err != nil {
		t.Fatal(err)
	}
	user, err := e.Auth.UserIDByName(ctx, "alice")
	if err != nil {
		t.Fatal(err)
	}
	// The grant starts at the subdirectory, not at the share root.
	if _, gerr := e.Core.CreateGrant(ctx, core.GrantSpec{
		User: &user, Share: sh.ID, Subpath: "allowed",
		Allow: everyPerm(), Inherit: true, Label: sh.Name,
	}); gerr != nil {
		t.Fatal(gerr)
	}
	if rerr := e.Core.ReloadGrants(ctx); rerr != nil {
		t.Fatal(rerr)
	}

	base := serve(t, e)
	sess := signIn(t, base, "alice", loginPassword)

	_, _, events := readSSE(t, base+"/api/v1/search/stream?q=target", sess)

	var inside, outside int
	for _, ev := range events {
		if ev.name != "hit" {
			continue
		}
		var hit map[string]any
		if uerr := json.Unmarshal([]byte(ev.data), &hit); uerr != nil {
			t.Fatal(uerr)
		}
		switch name := stringField(hit, "name"); name {
		case "target-inside.txt":
			inside++
		case "target-outside.txt":
			outside++
		}
	}

	if inside == 0 {
		t.Error("the file inside the granted subtree was not found, so this test shows nothing")
	}
	if outside != 0 {
		t.Error("a file above the granted subtree was returned, so the per-entry check did not run")
	}
}

// A file name carrying a line break cannot split the stream.
//
// The frame ends at a blank line, so a newline reaching the payload raw would
// end it early and the remainder would arrive as a second event the server
// never sent. A name is caller-supplied, so this would be a name deciding
// what frames a client receives.
//
// What actually prevents it is the JSON encoder, which escapes newlines
// inside a string. Removing the frame builder's own line-break check does not
// fail this test, and that was measured rather than assumed. The check is a
// second line behind the first, and this test pins the outcome rather than
// either mechanism, so it still holds if the encoder is ever swapped.
//
// Created on disk rather than through the API, because a newline cannot
// survive a URL path: the request line would be malformed before any handler
// saw it. It is legal in a POSIX name, so a share populated by any other
// means can hold one, and the walk will find it.
func TestAFileNameCannotSplitTheSearchStream(t *testing.T) {
	base, sess, share, host := contentShareAt(t, everyPerm(), []byte("unused"))

	nasty := "report\ndata: {\"name\":\"injected\"}\n\nevent: done\ndata: {}\n\n.txt"
	if werr := os.WriteFile(filepath.Join(host, nasty), []byte("x"), 0o600); werr != nil {
		t.Fatalf("creating the file: %v", werr)
	}
	_ = share // the share name is not needed: the file is placed on the host directly.

	status, _, events := readSSE(t, base+"/api/v1/search/stream?q=report", sess)
	if status != http.StatusOK {
		t.Fatalf("answered %d", status)
	}

	// Exactly one terminal event. A split frame is how a second one appears.
	var done, hits int
	for _, e := range events {
		switch e.name {
		case "done":
			done++
		case "hit":
			hits++
			var hit map[string]any
			if err := json.Unmarshal([]byte(e.data), &hit); err != nil {
				t.Errorf("a hit did not decode, so the frame was split: %q", e.data)
				continue
			}
			if stringField(hit, "name") == "injected" {
				t.Error("a file name produced a frame the server never sent")
			}
		}
	}
	if done != 1 {
		t.Errorf("%d terminal events, want exactly 1: the name split the stream", done)
	}
	if hits != 1 {
		t.Errorf("%d hits for one file, want exactly 1", hits)
	}
}

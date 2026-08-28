//go:build linux

package main

import (
	"bytes"
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/heavycaffeiner/stowcloud/go/engine/infra/vfs"
	"github.com/heavycaffeiner/stowcloud/go/engine/kit/secret"
	"github.com/heavycaffeiner/stowcloud/go/engine/kit/task"
	"github.com/heavycaffeiner/stowcloud/go/engine/service/acl"
	"github.com/heavycaffeiner/stowcloud/go/engine/service/auth"
	"github.com/heavycaffeiner/stowcloud/go/engine/service/core"
	"github.com/heavycaffeiner/stowcloud/go/engine/service/search"
	searchindex "github.com/heavycaffeiner/stowcloud/go/engine/service/search/index"
	searchsvc "github.com/heavycaffeiner/stowcloud/go/engine/service/search/svc"
	"github.com/heavycaffeiner/stowcloud/go/engine/service/settings/check"
	"github.com/heavycaffeiner/stowcloud/go/engine/service/settings/runtimecfg"
	"github.com/heavycaffeiner/stowcloud/go/engine/service/upload"
	"github.com/heavycaffeiner/stowcloud/go/engine/service/watch"
	"github.com/heavycaffeiner/stowcloud/go/engine/store/cache"
	"github.com/heavycaffeiner/stowcloud/go/engine/store/dbfile"
	"github.com/heavycaffeiner/stowcloud/go/engine/store/state"
)

// Every Phase 2 package has its own tests, and each builds only what it needs.
// That leaves a question none of them asks: do they compose? The layer rules
// say which package may import which and the gates enforce it, but an import
// graph is not evidence that two services agree about a share, a path or a
// permission when handed the same database and the same directory.
//
// This is the only place that can ask. The shipping binary still runs the old
// tree, because the cutover is Phase 3 by design, so nothing outside a test
// assembles the engine's service layer. The assembly is built here over one
// state database, one cache database, one ACL evaluator and one share on a real
// filesystem, and a workflow is run across it.
//
// The seam that matters most is search against the core. Search does not
// consult the permission model itself: the core hands it a per-source Allow
// closure, and every entry the walker considers goes through that closure. So
// the question is whether the closure the core builds actually excludes what
// the core would refuse, which no test on either side alone can answer.

const composeLabel = "documents"

// assembly is the Phase 2 service layer over one set of stores.
type assembly struct {
	state  *state.DB
	core   *core.Core
	auth   *auth.Service
	search *searchsvc.Service

	share core.ShareID
	root  string
	user  core.UserID
	dir   string
}

// compose builds the services a running deployment would have, wired to the
// same stores rather than to one fixture each.
func compose(t *testing.T) *assembly {
	t.Helper()
	ctx := t.Context()
	dir := t.TempDir()

	stf, err := dbfile.Open(ctx, state.Spec(filepath.Join(dir, "state.db")))
	if err != nil {
		t.Fatalf("opening the state database: %v", err)
	}
	t.Cleanup(func() {
		if cerr := stf.Close(); cerr != nil {
			t.Errorf("closing the state database: %v", cerr)
		}
	})
	st := state.New(stf)

	cf, err := dbfile.Open(ctx, cache.Spec(filepath.Join(dir, "cache.db")))
	if err != nil {
		t.Fatalf("opening the cache database: %v", err)
	}
	t.Cleanup(func() {
		if cerr := cf.Close(); cerr != nil {
			t.Errorf("closing the cache database: %v", cerr)
		}
	})
	ca, err := cache.New(ctx, cf, st)
	if err != nil {
		t.Fatalf("wrapping the cache: %v", err)
	}

	c, err := core.New(ctx, core.Options{State: st, Cache: ca, ACL: acl.NewEvaluator()})
	if err != nil {
		t.Fatalf("building the core: %v", err)
	}

	// The auth service over the state database the core reads: an account
	// created here is the account the core authorises.
	svc := auth.New(auth.Config{Store: st, StoreDir: dir})
	t.Setenv("SC_MASTER_KEY_FILE", filepath.Join(dir, "master.key"))
	if _, kerr := svc.OpenMasterKey(ctx); kerr != nil {
		t.Fatalf("OpenMasterKey: %v", kerr)
	}
	uid, err := svc.CreateAdmin(ctx, "operator", "", secret.New([]byte(testPassword)))
	if err != nil {
		t.Fatalf("CreateAdmin: %v", err)
	}

	shareRoot := t.TempDir()
	share, serr := c.CreateShare(ctx, core.ShareSpec{Name: composeLabel, Host: shareRoot})
	if serr != nil {
		t.Skipf("this host's temp directory is on a filesystem this build refuses: %v", serr)
	}

	ix, err := searchindex.Open(filepath.Join(dir, "searchindex"), searchindex.DefaultConfig())
	if err != nil {
		t.Fatalf("opening the search index: %v", err)
	}

	return &assembly{
		state:  st,
		core:   c,
		auth:   svc,
		search: searchsvc.New(searchsvc.Options{Index: ix}),
		share:  share.ID,
		root:   shareRoot,
		user:   core.UserID(uid),
		dir:    dir,
	}
}

// grant persists one grant and reloads the evaluator, which is the two-step
// discipline every grant write follows.
func (a *assembly) grant(t *testing.T, allow acl.Perms, at string) {
	t.Helper()
	holder := int64(a.user)
	if _, err := a.state.PersistGrant(t.Context(), state.GrantRow{
		User:    &holder,
		Share:   int64(a.share),
		Subpath: at,
		Allow:   uint16(allow),
		Inherit: true,
		Label:   composeLabel,
	}, 0); err != nil {
		t.Fatalf("persisting the grant: %v", err)
	}
	if err := a.core.ReloadGrants(t.Context()); err != nil {
		t.Fatalf("reloading the grants: %v", err)
	}
}

// write puts a file into the share through the filesystem, the way another
// program writing into a mounted share would.
func (a *assembly) write(t *testing.T, rel string) {
	t.Helper()
	full := filepath.Join(a.root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
}

// resolve asks the core for a capability the way a request does.
func (a *assembly) resolve(t *testing.T, rel string, need acl.Perms) (core.Resolved, error) {
	t.Helper()
	p, err := vfs.ParseVpath(composeLabel + "/" + rel)
	if err != nil {
		t.Fatalf("parsing %q: %v", rel, err)
	}
	return a.core.Resolve(a.user, p, need)
}

// build fills the index the way a deployment does, administrator-scoped: the
// index covers every share and the per-request closure is what narrows it.
func (a *assembly) build(t *testing.T) {
	t.Helper()
	if _, err := a.search.Build(t.Context(), search.SourcesOf(a.core.ScanSources()),
		func() bool { return true }, nil); err != nil {
		t.Fatalf("building the search index: %v", err)
	}
}

// found runs a query through the search service over the sources the core
// derives for one account, which is the wiring a request would use.
func (a *assembly) found(t *testing.T, query string) []string {
	t.Helper()
	sources := search.SourcesOf(a.core.UserScanSources(a.user))
	res, err := a.search.Query(t.Context(), sources,
		searchsvc.QueryOptions{Query: query, Limit: 1000})
	if err != nil {
		t.Fatalf("searching for %q: %v", query, err)
	}
	out := make([]string, 0, len(res.Hits))
	for _, h := range res.Hits {
		out = append(out, h.Path)
	}
	slices.Sort(out)
	return out
}

// The services build together over one set of stores, and each sees what the
// others wrote. A wiring mistake that each package's own fixture hides, because
// each builds only what it needs, shows up here.
func TestThePhase2ServicesComposeOverOneStore(t *testing.T) {
	a := compose(t)

	shares := a.core.Shares()
	if len(shares) != 1 || shares[0].Name != composeLabel {
		t.Errorf("the core sees %d share(s): %+v", len(shares), shares)
	}
	n, err := a.auth.CountUsers(t.Context())
	if err != nil || n != 1 {
		t.Errorf("the auth service counts %d account(s), %v", n, err)
	}
	// The core derives a scan source for the share, which is what search
	// consumes. A share the core knows and cannot hand to search is a share
	// nobody can search.
	if got := len(search.SourcesOf(a.core.ScanSources())); got != 1 {
		t.Errorf("the core produced %d scan source(s) for one share", got)
	}
}

// Everything search returns for an account resolves through the core for that
// account. This is the seam: search applies the core's Allow closure and the
// core enforces the same model on resolve, so a disagreement means a user is
// shown a hit they cannot open.
func TestEverySearchHitResolvesForTheSameUser(t *testing.T) {
	a := compose(t)
	a.grant(t, acl.Read|acl.Download, "")

	for _, p := range []string{
		"reports/annual.txt",
		"reports/quarterly/q1-summary.txt",
		"photos/holiday/beach.jpg",
		"notes.md",
	} {
		a.write(t, p)
	}

	a.build(t)

	hits := a.found(t, "annual")
	if len(hits) == 0 {
		t.Fatal("search found nothing, so the check below would prove nothing")
	}
	for _, h := range hits {
		if _, err := a.resolve(t, h, acl.Read); err != nil {
			t.Errorf("search returned %q and the core refuses it: %v", h, err)
		}
	}
}

// The other direction, which is the one that leaks: a subtree the account
// cannot read must not appear in its results.
//
// The grant starts partway down the tree, which is the case the core's comment
// calls out as the reason the check is per entry rather than per share.
func TestSearchDoesNotReturnASubtreeTheUserCannotRead(t *testing.T) {
	a := compose(t)
	// Readable only under reports/, so photos/ is invisible to this account.
	a.grant(t, acl.Read|acl.Download, "reports")

	a.write(t, "reports/annual-secret.txt")
	a.write(t, "photos/annual-private.jpg")

	a.build(t)

	// Both files exist and both match, so a walker ignoring the closure would
	// return two.
	hits := a.found(t, "annual")

	for _, h := range hits {
		if _, err := a.resolve(t, h, acl.Read); err != nil {
			t.Errorf("search returned %q, which the core refuses: %v", h, err)
		}
	}
	if slices.Contains(hits, "photos/annual-private.jpg") {
		t.Errorf("search returned a file outside the granted subtree: %v", hits)
	}
	if !slices.Contains(hits, "reports/annual-secret.txt") {
		t.Errorf("search lost a file inside the granted subtree: %v", hits)
	}
}

// An account with no grant at all sees nothing, through the same wiring.
func TestAnAccountWithNoGrantSearchesNothing(t *testing.T) {
	a := compose(t)
	a.write(t, "reports/annual.txt")
	a.build(t)

	if hits := a.found(t, "annual"); len(hits) != 0 {
		t.Errorf("an account with no grant found %v", hits)
	}
	// And the core agrees, so the empty result is the permission model rather
	// than an empty share.
	if _, err := a.resolve(t, "reports/annual.txt", acl.Read); err == nil {
		t.Error("the core resolved a path for an account with no grant")
	}
}

// The indexed tier is subject to the same closure as the walk. The index has no
// idea what a grant says, so if the filtering happened only on the walk an
// indexed deployment would leak exactly what an unindexed one does not.
func TestTheIndexedTierAppliesThePermissionCheckToo(t *testing.T) {
	a := compose(t)
	a.grant(t, acl.Read|acl.Download, "reports")

	a.write(t, "reports/annual-secret.txt")
	a.write(t, "photos/annual-private.jpg")

	a.build(t)

	sources := search.SourcesOf(a.core.UserScanSources(a.user))
	res, err := a.search.Query(t.Context(), sources,
		searchsvc.QueryOptions{Query: "annual", Limit: 1000})
	if err != nil {
		t.Fatal(err)
	}
	if res.Tier != searchsvc.TierIndex {
		t.Fatalf("the query was served by %s, so this says nothing about the indexed tier", res.Tier)
	}
	for _, h := range res.Hits {
		if h.Path == "photos/annual-private.jpg" {
			t.Error("the indexed tier returned a file outside the granted subtree")
		}
		if _, rerr := a.resolve(t, h.Path, acl.Read); rerr != nil {
			t.Errorf("the indexed tier returned %q, which the core refuses: %v", h.Path, rerr)
		}
	}
}

// A setting written through the checker is the setting the loader hands the
// services. A key one side writes and the other never reads is a control that
// silently does nothing.
func TestASettingWrittenIsASettingTheLoaderReads(t *testing.T) {
	a := compose(t)

	body := map[string]any{"max_concurrent_fast": float64(7)}
	findings := check.Section(check.Input{Section: "search", Body: body, DataDir: a.dir})
	if check.Blocked(findings) {
		t.Fatalf("the checker refused a valid value: %+v", check.Blocking(findings))
	}
	if err := a.state.MergeSettings(t.Context(), "search", body); err != nil {
		t.Fatalf("storing the setting: %v", err)
	}

	v := runtimecfg.Load(t.Context(), a.state, runtimecfg.Defaults(), nil)
	if v.SearchConcurrentSSD != 7 {
		t.Errorf("the loader read %d for a stored 7, so the control does nothing",
			v.SearchConcurrentSSD)
	}
}

// A key no loader reads is stored and then ignored, which is what makes the
// test above worth having.
//
// This is not hypothetical: the first version of that test wrote "concurrent"
// rather than "max_concurrent_fast", the store accepted it, and the loader
// returned the default. On a settings screen that is a control the operator
// moves and nothing happens.
//
// The store keeps whatever it is given, so this pins the shape of the problem
// rather than asserting a defence that does not exist.
func TestAKeyNoLoaderReadsIsStoredAndIgnored(t *testing.T) {
	a := compose(t)

	before := runtimecfg.Load(t.Context(), a.state, runtimecfg.Defaults(), nil)
	if err := a.state.MergeSettings(t.Context(), "search",
		map[string]any{"concurrent": float64(7)}); err != nil {
		t.Fatal(err)
	}

	doc, err := a.state.Settings(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	sec, ok := doc["search"].(map[string]any)
	if !ok {
		t.Fatalf("the section was not stored: %v", doc)
	}
	if _, present := sec["concurrent"]; !present {
		t.Fatal("the store dropped the key, so the rest of this no longer applies")
	}

	after := runtimecfg.Load(t.Context(), a.state, runtimecfg.Defaults(), nil)
	if after.SearchConcurrentSSD != before.SearchConcurrentSSD {
		t.Errorf("a key no loader reads changed the loaded value: %d became %d",
			before.SearchConcurrentSSD, after.SearchConcurrentSSD)
	}
}

// The checker and the loader agree about a bad value, so neither is the only
// thing holding the line.
func TestTheCheckerAndTheLoaderAgreeOnABadValue(t *testing.T) {
	a := compose(t)

	bad := map[string]any{"bind": "not a bind address:::"}
	if !check.Blocked(check.Section(check.Input{Section: "network", Body: bad, DataDir: a.dir})) {
		t.Error("the checker accepted a bind address nothing can bind")
	}

	// Stored anyway, as an older build or a hand-edited row would leave it.
	if err := a.state.MergeSettings(t.Context(), "network", bad); err != nil {
		t.Fatal(err)
	}
	defaults := runtimecfg.Defaults()
	if v := runtimecfg.Load(t.Context(), a.state, defaults, nil); v.Listen != defaults.Listen {
		t.Errorf("the loader started on a stored bad address: %q", v.Listen)
	}
}

// Disabling an account through the auth service stops it signing in, over the
// same database the core reads its grants from.
//
// A second, ordinary account is created for this: the assembly's own account is
// the only administrator, and the service refuses to disable the last one.
func TestDisablingAnAccountStopsItSigningIn(t *testing.T) {
	a := compose(t)

	uid, err := a.auth.CreateUser(t.Context(), "bob", "", secret.New([]byte(testPassword)))
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}

	if _, err := a.auth.Login(t.Context(), auth.LoginRequest{
		Name: "bob", Password: secret.New([]byte(testPassword)), IP: "127.0.0.1",
	}, 0); err != nil {
		t.Fatalf("the account could not sign in before being disabled: %v", err)
	}
	if err := a.auth.DisableAccount(t.Context(), uid); err != nil {
		t.Fatalf("DisableAccount: %v", err)
	}
	if _, err := a.auth.Login(t.Context(), auth.LoginRequest{
		Name: "bob", Password: secret.New([]byte(testPassword)), IP: "127.0.0.1",
	}, 0); err == nil {
		t.Error("a disabled account signed in")
	}
}

// The last administrator cannot be disabled, which is the refusal that made the
// test above need a second account.
//
// This is the same class of protection the emergency door exists for: a
// deployment nobody can administer is one nobody can repair through the
// interface, and the door only helps if an administrator can still sign in.
func TestTheLastAdministratorCannotBeDisabled(t *testing.T) {
	a := compose(t)

	if err := a.auth.DisableAccount(t.Context(), int64(a.user)); err == nil {
		t.Fatal("the only administrator was disabled, leaving nobody who can administer")
	}
	// And the account still works, so the refusal left nothing half-applied.
	if _, err := a.auth.Login(t.Context(), auth.LoginRequest{
		Name: "operator", Password: secret.New([]byte(testPassword)), IP: "127.0.0.1",
	}, 0); err != nil {
		t.Errorf("the refused disable broke the account anyway: %v", err)
	}
}

// The whole workflow across four packages: a file appears in the share, search
// finds it, the core resolves it, and the listing holds the same name.
func TestAFileIsFindableResolvableAndListable(t *testing.T) {
	a := compose(t)
	a.grant(t, acl.Read|acl.Download, "")

	if _, err := a.auth.Login(t.Context(), auth.LoginRequest{
		Name: "operator", Password: secret.New([]byte(testPassword)), IP: "127.0.0.1",
	}, 0); err != nil {
		t.Fatalf("signing in: %v", err)
	}

	a.write(t, "reports/annual.txt")
	a.build(t)

	hits := a.found(t, "annual")
	if !slices.Contains(hits, "reports/annual.txt") {
		t.Fatalf("search did not find the file: %v", hits)
	}

	if _, err := a.resolve(t, "reports/annual.txt", acl.Read); err != nil {
		t.Fatalf("the core refused the path search returned: %v", err)
	}

	// The listing of its directory holds it, so all three agree on the name
	// rather than two of them agreeing on a different spelling.
	at, err := a.resolve(t, "reports", acl.Read)
	if err != nil {
		t.Fatalf("resolving the directory: %v", err)
	}
	page, err := a.core.List(t.Context(), at, "")
	if err != nil {
		t.Fatalf("listing: %v", err)
	}
	var names []string
	for _, e := range page.Entries {
		names = append(names, e.Name)
	}
	if !slices.Contains(names, "annual.txt") {
		t.Errorf("the listing does not hold what search found: %v", names)
	}
}

// Upload against the rest of the assembly.
//
// This is the seam with the most moving parts. The upload engine resolves a
// destination through the core, writes into the same share the core owns, and
// publishes through the core's own path rather than renaming into place
// itself. Once it lands, the file has to be a file the core lists and search
// can index: three services agreeing on one path, none of which shares a test
// fixture with the others.
func TestAnUploadedFileIsListableAndFindable(t *testing.T) {
	a := compose(t)
	a.grant(t, acl.Read|acl.Write|acl.Create|acl.Download, "")

	up, err := upload.New(t.Context(), a.core, a.state, upload.Options{})
	if err != nil {
		t.Fatalf("building the upload engine: %v", err)
	}
	t.Cleanup(func() {
		if cerr := up.Close(); cerr != nil {
			t.Errorf("closing the upload engine: %v", cerr)
		}
	})

	// The destination directory exists, the way it does when a client uploads
	// into a folder it just listed. The refusal when it does not is the upload
	// package's own test.
	if derr := os.MkdirAll(filepath.Join(a.root, "incoming"), 0o755); derr != nil {
		t.Fatal(derr)
	}

	// The destination is resolved through the core, with the permission the
	// caller intends to exercise, which is how a request reaches it.
	dest, err := a.resolve(t, "incoming/report.txt", acl.Write|acl.Create)
	if err != nil {
		t.Fatalf("resolving the destination: %v", err)
	}

	body := []byte("the quarterly numbers")
	total := uint64(len(body))
	sess, err := up.Create(t.Context(), dest, upload.SessionSpec{TotalLen: &total})
	if err != nil {
		t.Fatalf("creating the session: %v", err)
	}

	if _, perr := up.PatchAt(t.Context(), dest.Root(), sess.ID, a.user, 0,
		bytes.NewReader(body), nil); perr != nil {
		t.Fatalf("writing the body: %v", perr)
	}
	if _, ferr := up.Finalize(t.Context(), dest, sess.ID); ferr != nil {
		t.Fatalf("finalizing: %v", ferr)
	}

	// The core lists it, which is the first agreement: the upload engine put it
	// where the core looks.
	at, err := a.resolve(t, "incoming", acl.Read)
	if err != nil {
		t.Fatalf("resolving the directory: %v", err)
	}
	page, err := a.core.List(t.Context(), at, "")
	if err != nil {
		t.Fatalf("listing: %v", err)
	}
	var names []string
	for _, e := range page.Entries {
		names = append(names, e.Name)
	}
	if !slices.Contains(names, "report.txt") {
		t.Fatalf("the core does not list the uploaded file: %v", names)
	}

	// Search finds it, which is the second: the walker sees the same tree the
	// upload wrote into.
	a.build(t)
	if hits := a.found(t, "report"); !slices.Contains(hits, "incoming/report.txt") {
		t.Errorf("search did not find the uploaded file: %v", hits)
	}

	// And the bytes are the ones that were sent, read back through the
	// filesystem rather than from the engine that claimed to have written them.
	got, err := os.ReadFile(filepath.Join(a.root, "incoming", "report.txt"))
	if err != nil {
		t.Fatalf("reading the uploaded file: %v", err)
	}
	if string(got) != string(body) {
		t.Errorf("the stored bytes are %q, want %q", got, body)
	}
}

// An upload into a share the account cannot write is refused by the core's
// resolve, before the upload engine is reached at all.
//
// The engine takes a resolved capability rather than a path, so the permission
// check cannot be skipped by calling it directly: there is no way to obtain the
// argument without passing the check.
func TestAnUploadWithoutWritePermissionIsRefused(t *testing.T) {
	a := compose(t)
	// Read only: enough to see the share, not to write into it.
	a.grant(t, acl.Read|acl.Download, "")

	if _, err := a.resolve(t, "incoming/report.txt", acl.Write|acl.Create); err == nil {
		t.Fatal("a read-only account resolved a write capability")
	}
	// And the file never appeared.
	if _, err := os.Stat(filepath.Join(a.root, "incoming", "report.txt")); err == nil {
		t.Error("a refused upload left a file behind")
	}
}

// A finalize with fewer bytes than declared refuses, and leaves nothing for the
// core to list or search to index.
//
// The engine's own tests cover the refusal. What this adds is that a refused
// upload does not become visible through the other two services, which is the
// property a user would notice: a truncated file appearing in a listing.
func TestAnIncompleteUploadBecomesVisibleToNobody(t *testing.T) {
	a := compose(t)
	a.grant(t, acl.Read|acl.Write|acl.Create|acl.Download, "")

	up, err := upload.New(t.Context(), a.core, a.state, upload.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if cerr := up.Close(); cerr != nil {
			t.Errorf("closing the upload engine: %v", cerr)
		}
	})

	if derr := os.MkdirAll(filepath.Join(a.root, "incoming"), 0o755); derr != nil {
		t.Fatal(derr)
	}
	dest, err := a.resolve(t, "incoming/partial.txt", acl.Write|acl.Create)
	if err != nil {
		t.Fatal(err)
	}
	total := uint64(100)
	sess, err := up.Create(t.Context(), dest, upload.SessionSpec{TotalLen: &total})
	if err != nil {
		t.Fatal(err)
	}
	// Ten of the hundred bytes promised.
	if _, perr := up.PatchAt(t.Context(), dest.Root(), sess.ID, a.user, 0,
		bytes.NewReader([]byte("0123456789")), nil); perr != nil {
		t.Fatal(perr)
	}
	if _, ferr := up.Finalize(t.Context(), dest, sess.ID); ferr == nil {
		t.Fatal("a short upload finalized")
	}

	a.build(t)
	if hits := a.found(t, "partial"); len(hits) != 0 {
		t.Errorf("search found an unfinished upload: %v", hits)
	}
	if _, err := os.Stat(filepath.Join(a.root, "incoming", "partial.txt")); err == nil {
		t.Error("an unfinished upload is on disk at its destination")
	}
}

// Preview's permission claim, against the real core.
//
// The service requires Read|Download, on the argument that a thumbnail derives
// from the bytes, so seeing one is seeing the file. That makes Download the
// dividing line: an account granted Read alone can list a file and open its
// metadata, and must not be able to see its contents through a thumbnail.
//
// No worker pool is needed to assert it. The check runs before the pool is
// reached, and before any cache lookup, which is the ordering the comment
// claims: a planted cache entry must not be a way in either.
func TestAViewOnlyAccountGetsNoThumbnail(t *testing.T) {
	a := compose(t)
	// Read without Download: the "view only" grant the permission model offers.
	a.grant(t, acl.Read, "")
	a.write(t, "photos/holiday.jpg")

	// The capability a thumbnail needs is refused at resolve, since Download is
	// not held. This is the whole of the check: the preview service requires
	// Read|Download and there is no way to obtain that capability here.
	if _, err := a.resolve(t, "photos/holiday.jpg", acl.Read|acl.Download); err == nil {
		t.Fatal("a view-only account resolved a download capability")
	}

	// The capability it can obtain lacks Download, so the requirement the
	// service applies to it fails. Asserted against the capability rather than
	// through the service, because reaching the service means spawning a
	// decoder, and the check runs before that.
	r, err := a.resolve(t, "photos/holiday.jpg", acl.Read)
	if err != nil {
		t.Fatalf("a view-only account could not resolve for reading: %v", err)
	}
	if err := r.Require(acl.Read | acl.Download); err == nil {
		t.Error("a view-only capability satisfies what a thumbnail requires")
	}
}

// A directory has no thumbnail, and the refusal comes after the permission
// check rather than instead of it.
func TestADirectoryHasNoThumbnail(t *testing.T) {
	a := compose(t)
	a.grant(t, acl.Read|acl.Download, "")
	a.write(t, "photos/holiday.jpg")

	// A downloading account holds what a thumbnail requires, so the refusal
	// below is about the target being a directory rather than about permission.
	r, err := a.resolve(t, "photos", acl.Read|acl.Download)
	if err != nil {
		t.Fatalf("resolving the directory: %v", err)
	}
	if rerr := r.Require(acl.Read | acl.Download); rerr != nil {
		t.Fatalf("the account does not hold what a thumbnail needs: %v", rerr)
	}
	st, serr := r.Root().Stat(r.Path())
	if serr != nil {
		t.Fatalf("stat: %v", serr)
	}
	if !st.Kind.IsDir() {
		t.Error("the resolved directory does not read as one, so preview would try to decode it")
	}
}

// The watcher and the search updater, which nothing connects yet.
//
// watch emits InvalEvent and the search updater consumes svc.Change. The two
// are field-for-field the same, which is not an accident: the watcher is what a
// deployment feeds the updater from, and the cutover in phase 3 is where that
// wiring lands. Until then nothing asserts the shapes still line up, so a field
// added to one and not the other is a compile error nobody sees until the day
// somebody writes the adapter.
//
// This is the adapter, written once here. If it stops compiling, the two have
// diverged and the phase 3 wiring is the thing that will have to change.
func TestAWatcherEventFeedsTheSearchUpdater(t *testing.T) {
	a := compose(t)
	a.grant(t, acl.Read|acl.Download, "")
	a.write(t, "reports/annual.txt")
	a.build(t)

	sink := make(chan watch.InvalEvent, 16)
	w, err := watch.Start(t.Context(), watch.DefaultConfig(), nil, sink)
	if err != nil {
		t.Skipf("this host cannot start a watcher: %v", err)
	}
	t.Cleanup(func() {
		if cerr := w.Close(); cerr != nil {
			t.Errorf("closing the watcher: %v", cerr)
		}
	})

	w.AddShare(vfs.ShareID(a.share), a.root, false)
	// Subscribed to the directory the file lands in, because a kernel watch is
	// per-directory: watching the share root reports changes to the root's own
	// entries and says nothing about what happens two levels down.
	reports, perr := vfs.ParseSafePath("reports")
	if perr != nil {
		t.Fatalf("parsing the watched directory: %v", perr)
	}
	w.Subscribe(vfs.ShareID(a.share), reports)

	// A file appears the way another program writing into the share makes one.
	a.write(t, "reports/brandnew.txt")

	// The event, converted to what the updater takes. This conversion is the
	// point of the test: it does not compile if the shapes drift apart.
	var change searchsvc.Change
	select {
	case ev := <-sink:
		change = searchsvc.Change{
			Share: uint32(ev.Share),
			Dir:   ev.Dir,
			All:   ev.All,
		}
	case <-time.After(10 * time.Second):
		t.Skip("no inotify event arrived; this host may not deliver them under test")
	}

	if change.Share != uint32(a.share) {
		t.Errorf("the event names share %d, want %d", change.Share, a.share)
	}

	// And the updater accepts it, which is the other half: the value converted
	// above is one the consumer will take.
	u := searchsvc.NewUpdater(a.search, func() []search.Source {
		return search.SourcesOf(a.core.ScanSources())
	}, slog.New(slog.DiscardHandler))
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	task.Go(ctx, "search updater", func() { u.Run(ctx); close(done) })
	u.Offer(change)

	// The file reaches the index, which is what the whole path exists to do.
	var indexed bool
	for range 500 {
		if slices.Contains(a.found(t, "brandnew"), "reports/brandnew.txt") {
			indexed = true
			break
		}
		time.Sleep(time.Millisecond)
	}
	cancel()
	<-done

	if !indexed {
		t.Error("a file the watcher reported never reached the index")
	}
}

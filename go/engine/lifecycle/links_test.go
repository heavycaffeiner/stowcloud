//go:build linux

package lifecycle_test

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/heavycaffeiner/stowcloud/go/engine/service/acl"
)

// mintLink creates a link over the share and returns the response body.
func mintLink(t *testing.T, base, token, share string) []byte {
	t.Helper()

	status, body := post(t, base+"/api/v1/links", token,
		map[string]any{"path": "/" + share + "/existing.txt", "label": "shared"})
	if status != http.StatusCreated {
		t.Fatalf("minting answered %d: %s", status, body)
	}
	return body
}

// A link token is returned once and never again. It is a credential in a URL:
// anyone holding it reaches the file, so a listing that carried it would put
// every link's secret behind one read of the listing.
func TestALinkTokenIsReturnedOnceAndNeverListed(t *testing.T) {
	base, token, share := shareWith(t, everyPerm())

	minted := mintLink(t, base, token, share)

	var created struct {
		Token string `json:"token"`
		Link  struct {
			ID string `json:"id"`
		} `json:"link"`
	}
	if err := json.Unmarshal(minted, &created); err != nil {
		t.Fatalf("the minted link does not parse: %v\n%s", err, minted)
	}
	if created.Token == "" {
		t.Fatalf("minting returned no token: %s", minted)
	}

	// The redaction the secret type produces under every formatting verb. If
	// this reached a client, the link would be unusable and the mistake would
	// look like a working response.
	if strings.Contains(created.Token, "REDACTED") || strings.Contains(created.Token, "redacted") {
		t.Fatalf("the response carries a redaction instead of the token: %s", minted)
	}

	// And the listing does not carry it.
	status, listing := authed(t, http.MethodGet, base+"/api/v1/links", token)
	if status != http.StatusOK {
		t.Fatalf("the listing answered %d: %s", status, listing)
	}
	if strings.Contains(string(listing), created.Token) {
		t.Errorf("the listing carries the link token: %s", listing)
	}
	for _, field := range []string{`"token"`, `"secret"`, `"password"`} {
		if strings.Contains(string(listing), field) {
			t.Errorf("the listing has a %s field: %s", field, listing)
		}
	}
}

// Minting needs Share, not Read. Publishing a file to anyone holding a URL is
// a different act from reading it, and an account that may read a share is not
// thereby allowed to publish it.
func TestMintingALinkNeedsShare(t *testing.T) {
	// Everything except Share.
	base, token, share := shareWith(t, everyPerm()&^acl.Share)

	status, body := post(t, base+"/api/v1/links", token,
		map[string]any{"path": "/" + share + "/existing.txt"})
	if status < 400 {
		t.Errorf("a link was minted without Share: %d %s", status, body)
	}

	// With Share: served, so the refusal above is about that bit.
	base2, token2, share2 := shareWith(t, everyPerm())
	ok, okBody := post(t, base2+"/api/v1/links", token2,
		map[string]any{"path": "/" + share2 + "/existing.txt"})
	if ok != http.StatusCreated {
		t.Errorf("minting was refused with every permission: %d %s", ok, okBody)
	}
}

// The link a person gets back is theirs, and appears in their own listing.
func TestAMintedLinkAppearsInItsOwnersListing(t *testing.T) {
	base, token, share := shareWith(t, everyPerm())

	mintLink(t, base, token, share)

	status, listing := authed(t, http.MethodGet, base+"/api/v1/links", token)
	if status != http.StatusOK {
		t.Fatalf("the listing answered %d: %s", status, listing)
	}

	var links []map[string]any
	if err := json.Unmarshal(listing, &links); err != nil {
		t.Fatalf("the listing does not parse: %v\n%s", err, listing)
	}
	if len(links) != 1 {
		t.Fatalf("the listing holds %d links, want the one just made", len(links))
	}
	if links[0]["label"] != "shared" {
		t.Errorf("the link's label is %v", links[0]["label"])
	}
}

// Deleting a link removes it. A revoke that reported success while leaving the
// link live is how a person believes they unpublished a file.
func TestDeletingALinkRemovesIt(t *testing.T) {
	base, token, share := shareWith(t, everyPerm())

	minted := mintLink(t, base, token, share)

	var created struct {
		Link struct {
			ID string `json:"id"`
		} `json:"link"`
	}
	if err := json.Unmarshal(minted, &created); err != nil {
		t.Fatal(err)
	}
	if created.Link.ID == "" {
		t.Fatalf("the minted link has no id: %s", minted)
	}

	status, body := authed(t, http.MethodDelete, base+"/api/v1/links/"+created.Link.ID, token)
	if status != http.StatusNoContent {
		t.Fatalf("delete answered %d: %s", status, body)
	}

	_, listing := authed(t, http.MethodGet, base+"/api/v1/links", token)
	var links []map[string]any
	if err := json.Unmarshal(listing, &links); err != nil {
		t.Fatal(err)
	}
	if len(links) != 0 {
		t.Errorf("the deleted link is still listed: %s", listing)
	}
}

// The link routes need a credential. A link listing served anonymously is
// every published URL on the deployment served anonymously.
func TestTheLinkRoutesNeedACredential(t *testing.T) {
	base, _, share := shareWith(t, everyPerm())

	status, body := get(t, base+"/api/v1/links")
	if status != http.StatusUnauthorized {
		t.Errorf("the listing answered %d anonymously: %s", status, body)
	}

	create, createBody := post(t, base+"/api/v1/links", "",
		map[string]any{"path": "/" + share + "/existing.txt"})
	if create != http.StatusUnauthorized {
		t.Errorf("minting answered %d anonymously: %s", create, createBody)
	}
}

// A link over a path this account cannot reach is refused.
func TestALinkCannotBeMintedOverAnUnreachablePath(t *testing.T) {
	base, token, _ := shareWith(t, everyPerm())

	for _, path := range []string{"/../etc/passwd", "/nothing/here", "/work/../../tmp"} {
		t.Run(path, func(t *testing.T) {
			status, body := post(t, base+"/api/v1/links", token,
				map[string]any{"path": path})
			if status < 400 {
				t.Errorf("%q was published: %d %s", path, status, body)
			}
		})
	}
}

// Deleting a link that is not there is refused rather than reported as done.
// A client told a revoke succeeded stops trying, and believes a URL it never
// unpublished is dead.
func TestDeletingAnAbsentLinkIsRefused(t *testing.T) {
	base, token, _ := shareWith(t, everyPerm())

	status, body := authed(t, http.MethodDelete, base+"/api/v1/links/999999", token)
	if status == http.StatusNoContent || status == http.StatusOK {
		t.Fatalf("deleting an absent link reported success: %d %s", status, body)
	}
	if status != http.StatusNotFound {
		t.Errorf("answered %d, want a not-found: %s", status, body)
	}
}

// One account cannot delete another's link. Publishing is the owner's decision
// and so is withdrawing it.
func TestOneAccountCannotDeleteAnothersLink(t *testing.T) {
	base, token, share := shareWith(t, everyPerm())

	minted := mintLink(t, base, token, share)
	var created struct {
		Link struct {
			ID string `json:"id"`
		} `json:"link"`
	}
	if err := json.Unmarshal(minted, &created); err != nil {
		t.Fatal(err)
	}

	// A second deployment's account, which is the strongest form of "not the
	// owner": a different id in a different database.
	other, otherToken, _ := shareWith(t, everyPerm())
	_ = other

	status, body := authed(t, http.MethodDelete, base+"/api/v1/links/"+created.Link.ID, otherToken)
	if status == http.StatusNoContent {
		t.Fatalf("another account's token deleted the link: %d %s", status, body)
	}

	// And the link is still there.
	_, listing := authed(t, http.MethodGet, base+"/api/v1/links", token)
	if !strings.Contains(string(listing), created.Link.ID) {
		t.Errorf("the link is gone after a refused delete: %s", listing)
	}
}

// linkID mints a link and returns its id.
func linkID(t *testing.T, base, token, share string) string {
	t.Helper()

	var created struct {
		Link struct {
			ID string `json:"id"`
		} `json:"link"`
	}
	if err := json.Unmarshal(mintLink(t, base, token, share), &created); err != nil {
		t.Fatal(err)
	}
	if created.Link.ID == "" {
		t.Fatal("the minted link has no id")
	}
	return created.Link.ID
}

// linkByID reads one link out of the listing.
func linkByID(t *testing.T, base, token, id string) map[string]any {
	t.Helper()

	status, body := authed(t, http.MethodGet, base+"/api/v1/links", token)
	if status != http.StatusOK {
		t.Fatalf("listing answered %d: %s", status, body)
	}
	var rows []map[string]any
	if err := json.Unmarshal(body, &rows); err != nil {
		t.Fatal(err)
	}
	for _, row := range rows {
		if stringField(row, "id") == id {
			return row
		}
	}
	t.Fatalf("no link with id %q in the listing", id)
	return nil
}

// patchLink sends an update with a raw JSON body, so a test can send an
// explicit null that a Go map cannot express distinctly.
func patchLink(t *testing.T, base, token, id, body string) (int, []byte) {
	t.Helper()

	req, err := http.NewRequest(http.MethodPatch,
		base+"/api/v1/links/"+id, strings.NewReader(body))
	if err != nil {
		t.Fatalf("building: %v", err)
	}
	req.SetBasicAuth("ignored", token)
	req.Header.Set("Content-Type", "application/json")

	resp, err := testClient().Do(req)
	if err != nil {
		t.Fatalf("requesting: %v", err)
	}
	defer func() {
		if cerr := resp.Body.Close(); cerr != nil {
			t.Errorf("closing: %v", cerr)
		}
	}()

	out := make([]byte, 0, 1024)
	buf := make([]byte, 1024)
	for {
		n, rerr := resp.Body.Read(buf)
		out = append(out, buf[:n]...)
		if rerr != nil {
			break
		}
	}
	return resp.StatusCode, out
}

// An update changes the label without touching anything else.
func TestUpdatingALinkLabel(t *testing.T) {
	base, token, share := shareWith(t, everyPerm())
	id := linkID(t, base, token, share)

	status, body := patchLink(t, base, token, id, `{"label":"renamed"}`)
	if status != http.StatusOK {
		t.Fatalf("updating answered %d: %s", status, body)
	}

	row := linkByID(t, base, token, id)
	if stringField(row, "label") != "renamed" {
		t.Errorf("the label is %v after a rename", row["label"])
	}
}

// An absent field leaves the value alone; an explicit null clears it.
//
// A single pointer cannot tell these apart, and the difference is not
// cosmetic: for a password, "leave it" and "remove it" are opposite decisions
// about who can open the link.
func TestAbsentAndNullMeanDifferentThings(t *testing.T) {
	base, token, share := shareWith(t, everyPerm())
	id := linkID(t, base, token, share)

	// Give it a password and an expiry to remove later.
	if status, body := patchLink(t, base, token, id,
		`{"password":"a-secret","expires_ns":"4102444800000000000"}`); status != http.StatusOK {
		t.Fatalf("setting answered %d: %s", status, body)
	}

	row := linkByID(t, base, token, id)
	if !boolField(row, "has_password") {
		t.Fatal("the password was not set")
	}
	if _, present := row["expires_ns"]; !present {
		t.Fatal("the expiry was not set")
	}

	// An update naming neither must leave both in place.
	if status, body := patchLink(t, base, token, id, `{"label":"still-locked"}`); status != http.StatusOK {
		t.Fatalf("the label-only update answered %d: %s", status, body)
	}
	row = linkByID(t, base, token, id)
	if !boolField(row, "has_password") {
		t.Error("an update that did not name the password removed it")
	}
	if _, present := row["expires_ns"]; !present {
		t.Error("an update that did not name the expiry removed it")
	}

	// An explicit null removes them.
	if status, body := patchLink(t, base, token, id,
		`{"password":null,"expires_ns":null}`); status != http.StatusOK {
		t.Fatalf("clearing answered %d: %s", status, body)
	}
	row = linkByID(t, base, token, id)
	if boolField(row, "has_password") {
		t.Error("an explicit null did not remove the password")
	}
	if _, present := row["expires_ns"]; present {
		t.Error("an explicit null did not remove the expiry")
	}
}

// An update never returns the token.
//
// It is a credential in a URL, returned once when the link is minted. A patch
// response carrying it would put every link's secret behind an ordinary edit.
func TestAnUpdateNeverReturnsTheToken(t *testing.T) {
	base, token, share := shareWith(t, everyPerm())

	minted := mintLink(t, base, token, share)
	var created struct {
		Token string `json:"token"`
		Link  struct {
			ID string `json:"id"`
		} `json:"link"`
	}
	if err := json.Unmarshal(minted, &created); err != nil {
		t.Fatal(err)
	}
	if created.Token == "" {
		t.Fatal("the mint returned no token, so this test cannot look for it")
	}

	status, body := patchLink(t, base, token, created.Link.ID, `{"label":"renamed"}`)
	if status != http.StatusOK {
		t.Fatalf("updating answered %d: %s", status, body)
	}
	if strings.Contains(string(body), created.Token) {
		t.Errorf("the update response carries the token: %s", body)
	}
	if strings.Contains(string(body), `"token"`) {
		t.Errorf("the update response has a token field: %s", body)
	}
}

// An update cannot widen a link's permissions.
//
// A link grants reading and downloading, decided when it is minted. If an
// update could add write, a URL handed to somebody would become a way to
// change the file, which is not what handing one out means.
func TestAnUpdateCannotWidenALink(t *testing.T) {
	base, token, share := shareWith(t, everyPerm())
	id := linkID(t, base, token, share)

	before := stringsOf(t, linkByID(t, base, token, id), "perms")

	// Whether this is refused or ignored does not matter; what matters is
	// that the stored permissions do not grow.
	patchLink(t, base, token, id, `{"perms":["read","write","create","delete"]}`)

	after := stringsOf(t, linkByID(t, base, token, id), "perms")
	if len(after) > len(before) {
		t.Errorf("the link went from %v to %v", before, after)
	}
	for _, p := range after {
		if p == "write" || p == "create" || p == "delete" {
			t.Errorf("the link now grants %q: %v", p, after)
		}
	}
}

// stringsOf reads a string list out of a decoded body.
func stringsOf(t *testing.T, body map[string]any, name string) []string {
	t.Helper()

	raw, ok := body[name].([]any)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(raw))
	for _, v := range raw {
		s, isString := v.(string)
		if !isString {
			t.Fatalf("%s holds a non-string %v", name, v)
		}
		out = append(out, s)
	}
	return out
}

// One account cannot edit another's link.
func TestUpdatingAnothersLinkIsRefused(t *testing.T) {
	base, token, share := shareWith(t, everyPerm())
	linkID(t, base, token, share)

	// A second engine's credential cannot be used here, so the check is that
	// an id belonging to nobody the caller owns is refused. The service takes
	// the owner alongside the id, which is what makes this a refusal rather
	// than a successful edit of a stranger's link.
	status, _ := patchLink(t, base, token, "999999", `{"label":"stolen"}`)
	if status == http.StatusOK {
		t.Error("an id the caller does not own was updated")
	}
}

// A 64-bit field is accepted as a decimal string and as a bare number.
//
// Every such value leaves this server as a string, because a JavaScript
// number loses exactness past 2^53. Accepting only a bare number on the way
// back makes the two directions disagree: a client that reads a link, edits
// its label and sends the object back is refused on a field it never touched.
func TestABigNumberIsAcceptedInBothSpellings(t *testing.T) {
	base, token, share := shareWith(t, everyPerm())

	// Past 2^53, which is where a JavaScript number stops being exact, and
	// far enough ahead that the service does not reject it as already past.
	// The value has to survive the round trip unchanged: read back as a
	// JavaScript number it would come back even, which is the whole reason
	// this API spells such values as strings.
	const exact = "9007199254740993000"

	for name, body := range map[string]string{
		"string": `{"expires_ns":"` + exact + `"}`,
		"number": `{"expires_ns":` + exact + `}`,
	} {
		t.Run(name, func(t *testing.T) {
			id := linkID(t, base, token, share)

			status, out := patchLink(t, base, token, id, body)
			if status != http.StatusOK {
				t.Fatalf("the %s spelling answered %d: %s", name, status, out)
			}

			row := linkByID(t, base, token, id)
			got := stringField(row, "expires_ns")
			if got != exact {
				t.Errorf("the %s spelling stored %q, want %q", name, got, exact)
			}
		})
	}
}

// A value that is not a number is refused rather than stored as zero.
//
// Zero is a real instant in 1970, so a link that took it would read as
// expired for ever while the caller was told the edit succeeded.
func TestANonNumericExpiryIsRefused(t *testing.T) {
	base, token, share := shareWith(t, everyPerm())
	id := linkID(t, base, token, share)

	for _, body := range []string{
		`{"expires_ns":"soon"}`,
		`{"expires_ns":"12x"}`,
		`{"expires_ns":true}`,
		`{"expires_ns":{}}`,
		`{"max_downloads":"many"}`,
	} {
		status, _ := patchLink(t, base, token, id, body)
		if status == http.StatusOK {
			t.Errorf("%s was accepted", body)
		}
	}

	// And nothing was stored, so the link is still the one that was minted.
	row := linkByID(t, base, token, id)
	if _, present := row["expires_ns"]; present {
		t.Errorf("a refused update set an expiry: %v", row["expires_ns"])
	}
}

// A download cap past its width is refused rather than wrapping.
func TestAnOversizedDownloadCapIsRefused(t *testing.T) {
	base, token, share := shareWith(t, everyPerm())
	id := linkID(t, base, token, share)

	// Past int32, which is what the cap is stored as. Truncating would turn a
	// large cap into a small one, or into a negative that never permits a
	// single download.
	status, body := patchLink(t, base, token, id, `{"max_downloads":"4294967297"}`)
	if status == http.StatusOK {
		t.Errorf("a cap past its width was accepted: %s", body)
	}

	row := linkByID(t, base, token, id)
	if _, present := row["max_downloads"]; present {
		t.Errorf("a refused update set a cap: %v", row["max_downloads"])
	}
}

// A link minted without a download cap is not capped at zero.
//
// The service spells unlimited as -1 and treats 0 as a real cap meaning no
// downloads at all. A request field that arrives as a plain zero when omitted
// therefore mints a link nobody can ever open, and the response says the link
// was created.
func TestALinkMintedWithoutACapIsNotCappedAtZero(t *testing.T) {
	base, token, share := shareWith(t, everyPerm())
	id := linkID(t, base, token, share)

	row := linkByID(t, base, token, id)
	if got := stringField(row, "max_downloads"); got == "0" {
		t.Errorf("a link minted with no cap allows %s downloads, so it can never be opened", got)
	}
}

// A link minted with an explicit cap keeps it.
//
// The fix for the absent cap must not turn every cap into unlimited, which
// would be the same defect pointing the other way: a link the owner limited
// to one download would serve for ever.
func TestAnExplicitDownloadCapIsKept(t *testing.T) {
	base, token, share := shareWith(t, everyPerm())

	status, body := post(t, base+"/api/v1/links", token, map[string]any{
		"path":          "/" + share + "/existing.txt",
		"max_downloads": 3,
	})
	if status != http.StatusCreated {
		t.Fatalf("minting answered %d: %s", status, body)
	}

	var created struct {
		Link struct {
			ID string `json:"id"`
		} `json:"link"`
	}
	if err := json.Unmarshal(body, &created); err != nil {
		t.Fatal(err)
	}

	row := linkByID(t, base, token, created.Link.ID)
	if got := stringField(row, "max_downloads"); got != "3" {
		t.Errorf("the cap is %q, want 3", got)
	}
}

// A cap of zero is stored when it is asked for.
//
// It means no downloads, which is a strange thing to want but a real one: it
// is how a link is disabled without being deleted. The absent case must not
// swallow it.
func TestAnExplicitZeroCapIsKept(t *testing.T) {
	base, token, share := shareWith(t, everyPerm())

	status, body := post(t, base+"/api/v1/links", token, map[string]any{
		"path":          "/" + share + "/existing.txt",
		"max_downloads": 0,
	})
	if status != http.StatusCreated {
		t.Fatalf("minting answered %d: %s", status, body)
	}

	var created struct {
		Link struct {
			ID string `json:"id"`
		} `json:"link"`
	}
	if err := json.Unmarshal(body, &created); err != nil {
		t.Fatal(err)
	}

	row := linkByID(t, base, token, created.Link.ID)
	if got := stringField(row, "max_downloads"); got != "0" {
		t.Errorf("an explicit cap of zero came back as %q", got)
	}
}

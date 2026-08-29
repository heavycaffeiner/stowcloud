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

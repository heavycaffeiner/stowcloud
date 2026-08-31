// Linux only, matching the package under test.
//go:build linux

package handler

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/heavycaffeiner/stowcloud/go/engine/kit/secret"
	"github.com/heavycaffeiner/stowcloud/go/engine/service/acl"
	"github.com/heavycaffeiner/stowcloud/go/engine/service/core"
)

const testNow = int64(1700000000000000000)

// No field of the owner-facing view can carry the token or its hash, whatever
// a caller passes. A listing is read on a screen, cached, and screenshotted;
// a live credential must not be in it.
func TestALinkListingCarriesNoCredential(t *testing.T) {
	tok := secret.New([]byte("this-is-the-token"))
	l := core.Link{
		ID:          7,
		Token:       &tok,
		TokenHash:   []byte("hash-bytes-that-authenticate-a-request"),
		HasPassword: true,
		Label:       "photos",
	}

	raw, err := json.Marshal(LinkOf(l, testNow))
	if err != nil {
		t.Fatalf("encoding: %v", err)
	}
	for _, leak := range []string{"this-is-the-token", "hash-bytes", "token", "hash"} {
		if strings.Contains(string(raw), leak) {
			t.Errorf("the listing carries %q: %s", leak, raw)
		}
	}

	// Whether a password is set is the one password fact that leaves the
	// service, because it changes what the screen offers.
	if !strings.Contains(string(raw), `"has_password":true`) {
		t.Errorf("the listing does not say a password is set: %s", raw)
	}
}

// The view type has no token field at all. A field cleared by each handler is
// a field one handler forgets; a type without one cannot leak it.
func TestTheLinkViewHasNoTokenField(t *testing.T) {
	rt := reflect.TypeOf(LinkView{})
	for i := range rt.NumField() {
		f := rt.Field(i)
		name := strings.ToLower(f.Name)
		// A boolean named for a password is the has-a-password fact, which is
		// the one thing that may cross. Anything else carrying these words is
		// a value, and a value is the credential.
		if strings.Contains(name, "password") && f.Type.Kind() == reflect.Bool {
			continue
		}
		for _, banned := range []string{"token", "secret", "hash", "password"} {
			if strings.Contains(name, banned) {
				t.Errorf("LinkView carries the field %s (%s)", f.Name, f.Type)
			}
		}
	}
}

// The mint response is the one place the token appears, and it is a separate
// type rather than a field that is usually empty.
func TestOnlyTheMintResponseCarriesTheToken(t *testing.T) {
	tok := secret.New([]byte("this-is-the-token"))
	got, ok := MintedLinkOf(core.Link{ID: 7, Token: &tok}, testNow)
	if !ok {
		t.Fatal("a link with a token could not be minted into a response")
	}
	if got.Token != "this-is-the-token" {
		t.Errorf("the mint response carries %q", got.Token)
	}

	// A link whose token could not be recovered reports so rather than
	// sending an empty string, which a client would try to use as a token.
	if _, legacy := MintedLinkOf(core.Link{ID: 8}, testNow); legacy {
		t.Error("a link with no recoverable token was minted anyway")
	}
}

// A link that never expires has no expiry, since zero would be a real instant
// in 1970 and every such link would read as long expired.
func TestALinkThatNeverExpiresHasNoExpiry(t *testing.T) {
	never := LinkOf(core.Link{Expires: 0, MaxDown: -1}, testNow)
	if never.ExpiresNs != nil || never.Expired {
		t.Errorf("a never-expiring link reports %v expired=%v", never.ExpiresNs, never.Expired)
	}
	if never.MaxDownloads != nil || never.Exhausted {
		t.Errorf("an uncapped link reports %v exhausted=%v", never.MaxDownloads, never.Exhausted)
	}

	raw, err := json.Marshal(never)
	if err != nil {
		t.Fatalf("encoding: %v", err)
	}
	if strings.Contains(string(raw), "expires_ns") || strings.Contains(string(raw), "max_downloads") {
		t.Errorf("a never-expiring uncapped link encoded limits: %s", raw)
	}
	// The download count is always present: zero downloads is a real answer.
	if !strings.Contains(string(raw), `"downloads":"0"`) {
		t.Errorf("the download count is missing: %s", raw)
	}
}

// Expired and exhausted are separate answers, because one is fixed by
// extending the link and the other by raising the cap.
func TestExpiredAndExhaustedAreSeparate(t *testing.T) {
	expired := LinkOf(core.Link{Expires: testNow - 1, MaxDown: 10, Downs: 3}, testNow)
	if !expired.Expired || expired.Exhausted {
		t.Errorf("an expired link reports expired=%v exhausted=%v", expired.Expired, expired.Exhausted)
	}

	spent := LinkOf(core.Link{Expires: testNow + 1, MaxDown: 3, Downs: 3}, testNow)
	if spent.Expired || !spent.Exhausted {
		t.Errorf("a spent link reports expired=%v exhausted=%v", spent.Expired, spent.Exhausted)
	}

	live := LinkOf(core.Link{Expires: testNow + 1, MaxDown: 10, Downs: 3}, testNow)
	if live.Expired || live.Exhausted {
		t.Errorf("a live link reports expired=%v exhausted=%v", live.Expired, live.Exhausted)
	}
}

// A drop link is marked, because inferring it from permission names and
// getting it wrong means showing a file browser for a mailbox.
func TestADropLinkIsMarked(t *testing.T) {
	drop := LinkOf(core.Link{Perms: acl.Create, MaxDown: -1}, testNow)
	if !drop.Drop {
		t.Error("a create-only link was not marked as a drop")
	}

	// Create alongside read is an ordinary link: the holder can list.
	both := LinkOf(core.Link{Perms: acl.Create | acl.Read, MaxDown: -1}, testNow)
	if both.Drop {
		t.Error("a link that can list was marked as a drop")
	}

	readOnly := LinkOf(core.Link{Perms: acl.Read | acl.Download, MaxDown: -1}, testNow)
	if readOnly.Drop {
		t.Error("a read-only link was marked as a drop")
	}
}

// One instant decides expiry for a whole listing, rather than each row
// drifting against its own read of the clock.
func TestAListingIsRenderedAgainstOneInstant(t *testing.T) {
	links := []core.Link{
		{ID: 1, Expires: testNow - 1, MaxDown: -1},
		{ID: 2, Expires: testNow + 1, MaxDown: -1},
	}
	got := LinksOf(links, testNow)
	if len(got) != 2 {
		t.Fatalf("the listing produced %d rows", len(got))
	}
	if !got[0].Expired || got[1].Expired {
		t.Errorf("the rows report expired=%v and %v", got[0].Expired, got[1].Expired)
	}

	raw, err := json.Marshal(LinksOf(nil, testNow))
	if err != nil {
		t.Fatalf("encoding: %v", err)
	}
	if string(raw) != "[]" {
		t.Errorf("an empty listing encoded as %s", raw)
	}
}

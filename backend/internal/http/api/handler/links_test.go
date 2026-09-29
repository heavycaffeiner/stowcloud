// Linux only, matching the package under test.
//go:build linux

package handler

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/heavycaffeiner/stowcloud/backend/internal/feature/files"
	secret "github.com/heavycaffeiner/stowcloud/backend/internal/platform/security/secret"
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

	raw, err := json.Marshal(LinkOf(l, "files/target.txt", testNow))
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
	got, ok := MintedLinkOf(core.Link{ID: 7, Token: &tok}, "files/target.txt", testNow)
	if !ok {
		t.Fatal("a link with a token could not be minted into a response")
	}
	if got.Token != "this-is-the-token" {
		t.Errorf("the mint response carries %q", got.Token)
	}

	// A link whose token could not be recovered reports so rather than
	// sending an empty string, which a client would try to use as a token.
	if _, legacy := MintedLinkOf(core.Link{ID: 8}, "files/target.txt", testNow); legacy {
		t.Error("a link with no recoverable token was minted anyway")
	}
}

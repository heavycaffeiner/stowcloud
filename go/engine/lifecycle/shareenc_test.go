//go:build linux

package lifecycle_test

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

// validSalt22 is a well-formed rclone password2: exactly 22 characters of
// base64url. Its content carries no meaning; only its shape is checked.
const validSalt22 = "AAAAAAAAAAAAAAAAAAAAAA"

// validVerifier is a well-formed verifier: 67 bytes starting with rclone's
// own file magic. The bytes after the header carry no meaning either; only
// the length and the prefix are checked, since nothing here can decrypt it.
func validVerifier() []byte {
	v := make([]byte, 67)
	copy(v, "RCLONE\x00\x00")
	return v
}

// enableEncryptionBody is the request enable takes, with every field
// defaulted to a well-formed value so a test can override just the one it is
// exercising.
func enableEncryptionBody() map[string]any {
	return map[string]any{
		"scheme":   "rclone-crypt-v1",
		"salt":     validSalt22,
		"verifier": base64.StdEncoding.EncodeToString(validVerifier()),
	}
}

// Enabling encryption on an empty share, then reading it back, round-trips
// every field byte for byte: the salt verbatim, since it is what the user
// types into rclone, and the verifier through base64, since JSON has no
// binary type.
func TestShareEncryptionEnableThenReadRoundTrips(t *testing.T) {
	base, cookie, csrf, _, _ := adminEngine(t)
	share := makeShare(t, base, cookie, csrf, "vault")

	if status, body := mutate(t, http.MethodPost, base+"/api/v1/encryption/"+share,
		cookie, csrf, enableEncryptionBody()); status != http.StatusNoContent {
		t.Fatalf("enabling answered %d: %v", status, body)
	}

	status, raw := withCookie(t, http.MethodGet, base+"/api/v1/encryption", cookie)
	if status != http.StatusOK {
		t.Fatalf("reading the list answered %d: %s", status, raw)
	}

	var page struct {
		Shares []struct {
			Share     float64  `json:"share"`
			Labels    []string `json:"labels"`
			Scheme    string   `json:"scheme"`
			Salt      string   `json:"salt"`
			Verifier  string   `json:"verifier"`
			CreatedNs float64  `json:"created_ns"`
		} `json:"shares"`
	}
	if perr := json.Unmarshal(raw, &page); perr != nil {
		t.Fatalf("decoding the list: %v: %s", perr, raw)
	}
	if len(page.Shares) != 1 {
		t.Fatalf("%d shares listed, want 1: %s", len(page.Shares), raw)
	}
	row := page.Shares[0]
	if row.Scheme != "rclone-crypt-v1" {
		t.Errorf("scheme round-tripped as %q", row.Scheme)
	}
	if row.Salt != validSalt22 {
		t.Errorf("salt round-tripped as %q, want the exact string the client sent", row.Salt)
	}
	decoded, derr := base64.StdEncoding.DecodeString(row.Verifier)
	if derr != nil {
		t.Fatalf("the verifier did not decode as base64: %v", derr)
	}
	if string(decoded) != string(validVerifier()) {
		t.Errorf("the verifier round-tripped as %x, want the exact 67 bytes sent", decoded)
	}
	if row.CreatedNs <= 0 {
		t.Errorf("created_ns is %v, want a positive instant", row.CreatedNs)
	}
	if len(row.Labels) != 1 || row.Labels[0] != "vault" {
		t.Errorf("labels are %v, want exactly [\"vault\"]", row.Labels)
	}
}

// A scheme this server does not record is refused, not silently accepted as
// whatever the client meant.
func TestShareEncryptionEnableRefusesWrongScheme(t *testing.T) {
	base, cookie, csrf, _, _ := adminEngine(t)
	share := makeShare(t, base, cookie, csrf, "vault")

	req := enableEncryptionBody()
	req["scheme"] = "age-v1"
	status, body := mutate(t, http.MethodPost, base+"/api/v1/encryption/"+share, cookie, csrf, req)
	if status != http.StatusUnprocessableEntity {
		t.Fatalf("a wrong scheme answered %d, want 422: %v", status, body)
	}
	if key := reasonKey(body); key != "encryption.invalid_scheme" {
		t.Errorf("the reason_key is %q, want encryption.invalid_scheme", key)
	}
}

// A salt of the wrong length or alphabet does not carry the entropy the
// design assumes, so it is refused rather than stored.
func TestShareEncryptionEnableRefusesMalformedSalt(t *testing.T) {
	for _, tc := range []struct {
		name string
		salt string
	}{
		{"too short", "short"},
		{"not base64url", strings.Repeat("A", 21) + "!"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			base, cookie, csrf, _, _ := adminEngine(t)
			share := makeShare(t, base, cookie, csrf, "vault")

			req := enableEncryptionBody()
			req["salt"] = tc.salt
			status, body := mutate(t, http.MethodPost, base+"/api/v1/encryption/"+share, cookie, csrf, req)
			if status != http.StatusUnprocessableEntity {
				t.Fatalf("a malformed salt answered %d, want 422: %v", status, body)
			}
			if key := reasonKey(body); key != "encryption.invalid_salt" {
				t.Errorf("the reason_key is %q, want encryption.invalid_salt", key)
			}
		})
	}
}

// A verifier that is not 67 bytes beginning with rclone's own magic is
// refused, whether it fails to decode at all or decodes to the wrong shape.
func TestShareEncryptionEnableRefusesMalformedVerifier(t *testing.T) {
	for _, tc := range []struct {
		name     string
		verifier string
	}{
		{"not base64", "not valid base64!!"},
		{"wrong length", base64.StdEncoding.EncodeToString([]byte("too short"))},
		{"wrong magic", base64.StdEncoding.EncodeToString(make([]byte, 67))},
	} {
		t.Run(tc.name, func(t *testing.T) {
			base, cookie, csrf, _, _ := adminEngine(t)
			share := makeShare(t, base, cookie, csrf, "vault")

			req := enableEncryptionBody()
			req["verifier"] = tc.verifier
			status, body := mutate(t, http.MethodPost, base+"/api/v1/encryption/"+share, cookie, csrf, req)
			if status != http.StatusUnprocessableEntity {
				t.Fatalf("a malformed verifier answered %d, want 422: %v", status, body)
			}
			if key := reasonKey(body); key != "encryption.invalid_verifier" {
				t.Errorf("the reason_key is %q, want encryption.invalid_verifier", key)
			}
		})
	}
}

// A share holding anything a client can see cannot have encryption turned
// on: plaintext already written under it would sit beside ciphertext with
// nothing on disk saying which was which.
func TestShareEncryptionEnableRefusesNonEmptyShare(t *testing.T) {
	base, cookie, csrf, _, _ := adminEngine(t)
	share := makeShare(t, base, cookie, csrf, "vault")

	if status, body := mutate(t, http.MethodPost, base+"/api/v1/files/mkdir", cookie, csrf,
		map[string]string{"path": "/vault/inside"}); status != http.StatusCreated {
		t.Fatalf("seeding the share answered %d: %v", status, body)
	}

	status, body := mutate(t, http.MethodPost, base+"/api/v1/encryption/"+share, cookie, csrf, enableEncryptionBody())
	if status != http.StatusUnprocessableEntity {
		t.Fatalf("enabling over a non-empty share answered %d, want 422: %v", status, body)
	}
	if key := reasonKey(body); key != "encryption.share_not_empty" {
		t.Errorf("the reason_key is %q, want encryption.share_not_empty", key)
	}
}

// An id naming no share at all is not found, the same as every other
// admin/{id} route on this surface.
func TestShareEncryptionEnableRefusesUnknownShare(t *testing.T) {
	base, cookie, csrf, _, _ := adminEngine(t)

	status, body := mutate(t, http.MethodPost, base+"/api/v1/encryption/999999", cookie, csrf, enableEncryptionBody())
	if status != http.StatusNotFound {
		t.Fatalf("an unknown share answered %d, want 404: %v", status, body)
	}
}

// Disabling is idempotent: a share that was never encrypted, and one that
// was and now is not, both answer success rather than the caller having to
// know which case it is in before asking.
func TestShareEncryptionDisableIsIdempotent(t *testing.T) {
	base, cookie, csrf, _, _ := adminEngine(t)
	share := makeShare(t, base, cookie, csrf, "vault")

	// Never encrypted.
	if status, body := mutate(t, http.MethodDelete, base+"/api/v1/encryption/"+share,
		cookie, csrf, nil); status != http.StatusNoContent {
		t.Fatalf("disabling an unencrypted share answered %d, want 204: %v", status, body)
	}

	if status, body := mutate(t, http.MethodPost, base+"/api/v1/encryption/"+share,
		cookie, csrf, enableEncryptionBody()); status != http.StatusNoContent {
		t.Fatalf("enabling answered %d: %v", status, body)
	}

	// Encrypted, then turned off, then turned off again.
	if status, body := mutate(t, http.MethodDelete, base+"/api/v1/encryption/"+share,
		cookie, csrf, nil); status != http.StatusNoContent {
		t.Fatalf("the first disable answered %d, want 204: %v", status, body)
	}
	if status, body := mutate(t, http.MethodDelete, base+"/api/v1/encryption/"+share,
		cookie, csrf, nil); status != http.StatusNoContent {
		t.Fatalf("the second disable answered %d, want 204: %v", status, body)
	}
}

// Neither route reaches the service for an account that does not administer
// this deployment, the same gate every admin/* mutation demands.
func TestShareEncryptionEnableAndDisableRefuseNonAdmin(t *testing.T) {
	base, cookie, csrf, plainCookie, plainCSRF := adminEngine(t)
	share := makeShare(t, base, cookie, csrf, "vault")

	if status, body := mutate(t, http.MethodPost, base+"/api/v1/encryption/"+share,
		plainCookie, plainCSRF, enableEncryptionBody()); status != http.StatusForbidden {
		t.Errorf("enable answered %d for a non-administrator, want 403: %v", status, body)
	}
	if status, body := mutate(t, http.MethodDelete, base+"/api/v1/encryption/"+share,
		plainCookie, plainCSRF, nil); status != http.StatusForbidden {
		t.Errorf("disable answered %d for a non-administrator, want 403: %v", status, body)
	}
}

// The read is for any authenticated caller, but each sees only what their
// own grants project: an encrypted share nobody granted them stays invisible,
// the same rule the file listing enforces.
func TestShareEncryptionListFiltersByGrant(t *testing.T) {
	base, cookie, csrf, plainCookie, _ := adminEngine(t)

	visible := makeShare(t, base, cookie, csrf, "visible")
	hidden := makeShare(t, base, cookie, csrf, "hidden")

	for _, share := range []string{visible, hidden} {
		if status, body := mutate(t, http.MethodPost, base+"/api/v1/encryption/"+share,
			cookie, csrf, enableEncryptionBody()); status != http.StatusNoContent {
			t.Fatalf("enabling share %s answered %d: %v", share, status, body)
		}
	}

	user := accountID(t, base, cookie, loginName)
	if status, body := mutate(t, http.MethodPost, base+"/api/v1/admin/grants", cookie, csrf,
		map[string]any{
			"user": user, "share": visible, "subpath": "",
			"allow": []string{"read"}, "inherit": true, "label": "visible",
		}); status != http.StatusCreated {
		t.Fatalf("granting access answered %d: %v", status, body)
	}

	status, raw := withCookie(t, http.MethodGet, base+"/api/v1/encryption", plainCookie)
	if status != http.StatusOK {
		t.Fatalf("the plain account's read answered %d: %s", status, raw)
	}
	var page struct {
		Shares []struct {
			Labels []string `json:"labels"`
		} `json:"shares"`
	}
	if perr := json.Unmarshal(raw, &page); perr != nil {
		t.Fatalf("decoding the list: %v: %s", perr, raw)
	}
	if len(page.Shares) != 1 {
		t.Fatalf("the plain account's read lists %d shares, want exactly the one it was granted: %s",
			len(page.Shares), raw)
	}
	if len(page.Shares[0].Labels) != 1 || page.Shares[0].Labels[0] != "visible" {
		t.Errorf("the listed share's labels are %v, want [\"visible\"]", page.Shares[0].Labels)
	}
}

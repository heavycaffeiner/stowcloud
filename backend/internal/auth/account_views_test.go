// Linux only, matching the package under test.
//go:build linux

package auth

import (
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

// The revocation handle is derived from the stored digest, not the digest
// itself. Publishing the digest would hand out the value the store compares
// against, which is what authenticates a lookup.
func TestTheSessionHandleIsNotTheStoredDigest(t *testing.T) {
	digest := []byte("the-stored-digest-bytes")
	v := SessionOf(SessionRow{IDHash: digest}, nil)

	// Not the digest in any encoding. Checking only the raw bytes would pass a
	// handle that is the digest in hex, which is the same value handed out in
	// a different alphabet.
	for _, spelling := range []string{
		string(digest),
		hex.EncodeToString(digest),
		base64.StdEncoding.EncodeToString(digest),
		base64.RawURLEncoding.EncodeToString(digest),
	} {
		if v.Handle == spelling {
			t.Fatalf("the handle is the stored digest, spelled %q", spelling)
		}
	}

	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("encoding: %v", err)
	}
	if strings.Contains(string(raw), "the-stored-digest") ||
		strings.Contains(string(raw), hex.EncodeToString(digest)) {
		t.Errorf("the listing carries the digest: %s", raw)
	}

	// Stable, so a client can revoke with what it was shown.
	if SessionOf(SessionRow{IDHash: digest}, nil).Handle != v.Handle {
		t.Error("the handle is not stable across projections")
	}
	// Different sessions get different handles, or revocation would hit the
	// wrong one.
	other := SessionOf(SessionRow{IDHash: []byte("another-digest")}, nil)
	if other.Handle == v.Handle {
		t.Error("two sessions share a handle")
	}
}

// Neither view type has a field that could hold a credential.
func TestTheAccountViewsCarryNoCredential(t *testing.T) {
	for _, rt := range []reflect.Type{
		reflect.TypeOf(SessionView{}),
		reflect.TypeOf(AppPasswordView{}),
	} {
		for i := range rt.NumField() {
			f := rt.Field(i)
			name := strings.ToLower(f.Name)
			for _, banned := range []string{"token", "secret", "password", "digest", "idhash"} {
				if strings.Contains(name, banned) {
					t.Errorf("%s carries the field %s (%s)", rt.Name(), f.Name, f.Type)
				}
			}
		}
	}
}

// The session making the request is marked, so a client can warn before
// signing itself out.
func TestTheCurrentSessionIsMarked(t *testing.T) {
	mine := []byte("my-session-digest")
	rows := []SessionRow{
		{IDHash: []byte("another-device")},
		{IDHash: mine},
	}

	got := SessionsOf(rows, mine)
	if len(got) != 2 {
		t.Fatalf("the listing produced %d rows", len(got))
	}
	if got[0].Current {
		t.Error("another device was marked as the current session")
	}
	if !got[1].Current {
		t.Error("the current session was not marked")
	}

	// With no current session known, nothing is marked rather than everything.
	none := SessionsOf(rows, nil)
	for i, r := range none {
		if r.Current {
			t.Errorf("row %d was marked current with no session known", i)
		}
	}
}

// Linux only, matching the package under test.
//go:build linux

package handler

import (
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/heavycaffeiner/stowcloud/go/engine/service/acl"
	"github.com/heavycaffeiner/stowcloud/go/engine/service/auth"
)

// The revocation handle is derived from the stored digest, not the digest
// itself. Publishing the digest would hand out the value the store compares
// against, which is what authenticates a lookup.
func TestTheSessionHandleIsNotTheStoredDigest(t *testing.T) {
	digest := []byte("the-stored-digest-bytes")
	v := SessionOf(auth.SessionRow{IDHash: digest}, nil)

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
	if SessionOf(auth.SessionRow{IDHash: digest}, nil).Handle != v.Handle {
		t.Error("the handle is not stable across projections")
	}
	// Different sessions get different handles, or revocation would hit the
	// wrong one.
	other := SessionOf(auth.SessionRow{IDHash: []byte("another-digest")}, nil)
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
	rows := []auth.SessionRow{
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

// An app password lists what it can reach, and an empty share list means every
// share the account reaches rather than none.
func TestAnAppPasswordListsItsScope(t *testing.T) {
	v := AppPasswordOf(auth.AppPasswordRow{
		ID:         7,
		Name:       "phone",
		ScopePerms: uint16(acl.Read | acl.Download),
		Shares:     []string{"3", "5"},
	})

	if v.ID != "7" || v.Name != "phone" {
		t.Errorf("the credential projected as %+v", v)
	}
	if len(v.Perms) != 2 || v.Perms[0] != "read" || v.Perms[1] != "download" {
		t.Errorf("the permissions are %v", v.Perms)
	}
	if len(v.Shares) != 2 {
		t.Errorf("the shares are %v", v.Shares)
	}

	// No shares encodes as an empty list rather than null, so a client
	// iterating it does not have to test the field.
	raw, err := json.Marshal(AppPasswordOf(auth.AppPasswordRow{}))
	if err != nil {
		t.Fatalf("encoding: %v", err)
	}
	if !strings.Contains(string(raw), `"shares":[]`) {
		t.Errorf("an unscoped credential encoded as %s", raw)
	}
}

// A credential that never expires or was never used says nothing rather than
// zero, since zero is a real instant and would read as 1970.
func TestAbsentTimesStayAbsent(t *testing.T) {
	v := AppPasswordOf(auth.AppPasswordRow{})
	if v.ExpiresNs != nil || v.LastUsedNs != nil {
		t.Errorf("a fresh credential reports %v and %v", v.ExpiresNs, v.LastUsedNs)
	}

	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("encoding: %v", err)
	}
	if strings.Contains(string(raw), "expires_ns") || strings.Contains(string(raw), "last_used_ns") {
		t.Errorf("a fresh credential encoded absent times: %s", raw)
	}

	// A real epoch value is present and is zero.
	var epoch int64
	used := AppPasswordOf(auth.AppPasswordRow{LastUsedNs: &epoch})
	if used.LastUsedNs == nil || *used.LastUsedNs != "0" {
		t.Errorf("a real epoch use encoded as %v", used.LastUsedNs)
	}
}

// The projection copies the share slice rather than aliasing the service's.
func TestTheAppPasswordDoesNotAliasTheService(t *testing.T) {
	row := auth.AppPasswordRow{Shares: []string{"3"}}
	v := AppPasswordOf(row)
	row.Shares[0] = "mutated"

	if v.Shares[0] != "3" {
		t.Errorf("the view aliased the service's slice: %v", v.Shares)
	}
}

// Empty listings encode as lists.
func TestEmptyAccountListingsEncodeAsLists(t *testing.T) {
	for _, v := range []any{SessionsOf(nil, nil), AppPasswordsOf(nil)} {
		raw, err := json.Marshal(v)
		if err != nil {
			t.Fatalf("encoding: %v", err)
		}
		if string(raw) != "[]" {
			t.Errorf("an empty listing encoded as %s", raw)
		}
	}
}

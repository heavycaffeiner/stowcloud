//go:build compat_nc

package nc

import (
	"context"
	"net/http"
	"testing"

	"github.com/heavycaffeiner/stowcloud/go/internal/compat/ncport"
)

// The existence rule survives the translation.
//
// A path outside a grant is not-found on every compat mount too, and the test
// for that is a table run across the mounts rather than against the native API
// alone. WebDAV's and OCS's status vocabularies both make a forbidden feel
// natural here, and it is wrong: a refusal that distinguishes "you may not see
// this" from "this does not exist" tells a stranger which files exist.

// denyingAccounts is an account port that can see nothing, which is what a
// caller looking outside their own scope reaches.
type denyingAccounts struct{}

func (denyingAccounts) UserInfo(context.Context, ncport.UserID) (ncport.UserInfo, error) {
	return ncport.UserInfo{LoginName: "self"}, nil
}
func (denyingAccounts) Quota(context.Context, ncport.UserID) (ncport.Quota, error) {
	return ncport.Quota{}, nil
}
func (denyingAccounts) UserInfoByLogin(context.Context, ncport.UserID, string) (ncport.UserInfo, bool, error) {
	// Outside scope, which is the same answer as absent.
	return ncport.UserInfo{}, false, nil
}

// denyingDirect resolves nothing, which is what a file id outside the caller's
// grants looks like.
type denyingDirect struct{}

func (denyingDirect) Locate(context.Context, ncport.UserID, FileID) (string, error) {
	return "", context.Canceled
}
func (denyingDirect) SignedDownloadURL(context.Context, ncport.UserID, string) (string, bool, error) {
	return "", false, nil
}

func existenceLayer(t *testing.T) *Layer {
	t.Helper()
	return New(Deps{
		Authenticate: func(*http.Request) (Principal, bool) { return Principal{User: 1}, true },
		Accounts:     denyingAccounts{},
		Direct:       denyingDirect{},
	})
}

// An account the caller may not see is not-found, never forbidden. A forbidden
// confirms the login exists, which is the enumeration this closes.
func TestAnAccountOutsideScopeIsNotFound(t *testing.T) {
	l := existenceLayer(t)
	rec := serve(t, l, "GET", "/ocs/v2.php/cloud/users/somebody?format=json")

	if rec.Code == http.StatusForbidden {
		t.Fatal("an account outside scope answered forbidden, which confirms it exists")
	}
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404\n%s", rec.Code, rec.Body)
	}
}

// And an account that genuinely does not exist answers identically, which is
// what makes the two indistinguishable.
func TestAnAbsentAccountAnswersTheSameAsAnInvisibleOne(t *testing.T) {
	l := existenceLayer(t)
	invisible := serve(t, l, "GET", "/ocs/v2.php/cloud/users/invisible?format=json")
	absent := serve(t, l, "GET", "/ocs/v2.php/cloud/users/absent?format=json")

	if invisible.Code != absent.Code {
		t.Fatalf("an invisible account answered %d and an absent one %d",
			invisible.Code, absent.Code)
	}
	if invisible.Body.String() != absent.Body.String() {
		t.Fatalf("the two answers differ:\n%s\n%s", invisible.Body, absent.Body)
	}
}

// A file id the caller cannot read is not-found. A forbidden would confirm the
// id names something, which is the whole reason this endpoint is the sharpest
// thing in the layer.
func TestAFileIDOutsideTheCallersGrantsIsNotFound(t *testing.T) {
	l := existenceLayer(t)
	rec := serve(t, l, "POST", "/ocs/v2.php/apps/dav/api/v1/direct?format=json&fileId=12345")

	if rec.Code == http.StatusForbidden {
		t.Fatal("an invisible file id answered forbidden, which confirms it exists")
	}
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404\n%s", rec.Code, rec.Body)
	}
}

// A malformed file id answers the same as an invisible one, so a caller cannot
// learn that an id was well formed.
func TestAMalformedFileIDAnswersLikeAnInvisibleOne(t *testing.T) {
	l := existenceLayer(t)
	malformed := serve(t, l, "POST", "/ocs/v2.php/apps/dav/api/v1/direct?format=json&fileId=notanumber")
	invisible := serve(t, l, "POST", "/ocs/v2.php/apps/dav/api/v1/direct?format=json&fileId=12345")

	if malformed.Code != invisible.Code {
		t.Fatalf("a malformed id answered %d and an invisible one %d",
			malformed.Code, invisible.Code)
	}
}

// Every mount answers the same way for something the caller cannot see, which
// is the table the proposal asks for rather than one surface's behaviour.
func TestTheExistenceRuleHoldsAcrossTheMounts(t *testing.T) {
	l := existenceLayer(t)

	for _, tc := range []struct {
		name, method, path string
	}{
		{"an unrouted OCS path", "GET", "/ocs/v2.php/apps/nothing/here"},
		{"an account outside scope", "GET", "/ocs/v2.php/cloud/users/somebody"},
		{"a file id outside a grant", "POST", "/ocs/v2.php/apps/dav/api/v1/direct?fileId=1"},
		{"a recorded-crash path", "GET", "/ocs/v2.php/apps/activity/api/v2/activity"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sep := "?"
			if containsQuestion(tc.path) {
				sep = "&"
			}
			rec := serve(t, l, tc.method, tc.path+sep+"format=json")
			if rec.Code == http.StatusForbidden {
				t.Fatalf("%s answered forbidden, which tells a stranger it exists", tc.name)
			}
			if rec.Code != http.StatusNotFound {
				t.Fatalf("%s answered %d, want 404\n%s", tc.name, rec.Code, rec.Body)
			}
		})
	}
}

func containsQuestion(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] == '?' {
			return true
		}
	}
	return false
}

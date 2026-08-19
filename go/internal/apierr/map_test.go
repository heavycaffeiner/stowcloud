package apierr

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"testing"

	"github.com/heavycaffeiner/stowcloud/go/internal/auth"
	"github.com/heavycaffeiner/stowcloud/go/internal/core"
	"github.com/heavycaffeiner/stowcloud/go/internal/limits"
	"github.com/heavycaffeiner/stowcloud/go/internal/vfs"
)

// The existence rule table. It is a table over every domain error asserting
// the status and the wire code, and one row of it is the rule's shape: the
// denied refusal that a caller may not know about is byte-identical to a
// missing path, so it lives on the same row as ErrNotFound.
func TestMapTable(t *testing.T) {
	tests := []struct {
		name string
		err  error
		code Code
		// want is the status the case must map to.
		want int
	}{
		{"not found", core.ErrNotFound, CodeFsNotFound, http.StatusNotFound},
		{"vfs not found", vfs.ErrNotFound, CodeFsNotFound, http.StatusNotFound},
		{"symlink refused is unlistable", vfs.ErrSymlinkDenied, CodeFsNotFound, http.StatusNotFound},
		{"denied", core.ErrDenied, CodeACLDenied, http.StatusForbidden},
		{"vfs denied", vfs.ErrDenied, CodeACLDenied, http.StatusForbidden},
		{"precondition", core.ErrPrecondition, CodeFsPrecondition, http.StatusPreconditionFailed},
		{"precondition with token", &core.PreconditionError{Current: "w1"}, CodeFsPrecondition, http.StatusPreconditionFailed},
		{"conflict", core.ErrConflict, CodeFsConflict, http.StatusConflict},
		{"cross device", vfs.ErrCrossDevice, CodeFsConflict, http.StatusConflict},
		{"exists", core.ErrExists, CodeFsConflict, http.StatusConflict},
		{"vfs exists", vfs.ErrExists, CodeFsConflict, http.StatusConflict},
		{"not empty", core.ErrNotEmpty, CodeFsConflict, http.StatusConflict},
		{"vfs not empty", vfs.ErrNotEmpty, CodeFsConflict, http.StatusConflict},
		{"cross share", core.ErrCrossShare, CodeFsConflict, http.StatusConflict},
		{"not a directory", vfs.ErrNotADirectory, CodeFsConflict, http.StatusConflict},
		{"trash disabled", core.ErrTrashDisabled, CodeFsConflict, http.StatusConflict},
		{"link gone", core.ErrLinkExpired, CodeFsGone, http.StatusGone},
		{"no space", core.ErrNoSpace, CodeQuotaExceeded, http.StatusInsufficientStorage},
		{"vfs no space", vfs.ErrNoSpace, CodeQuotaExceeded, http.StatusInsufficientStorage},
		{"quota", core.ErrQuotaExceeded, CodeQuotaExceeded, http.StatusInsufficientStorage},
		{"limit exceeded", limits.Exceed("RequestBody", 1<<20, 1<<21), CodeLimitExceeded, http.StatusRequestEntityTooLarge},
		{"invalid name", &vfs.NameError{Component: "con", Reason: "a device name", Err: vfs.ErrInvalidName}, CodeFsInvalidName, http.StatusUnprocessableEntity},
		{"reserved name", &vfs.NameError{Component: ".scmeta", Reason: "a control prefix", Err: vfs.ErrReservedName}, CodeFsInvalidName, http.StatusUnprocessableEntity},
		{"credentials", auth.ErrCredentials, CodeAuthInvalid, http.StatusUnauthorized},
		{"account disabled", auth.ErrAccountDisabled, CodeACLDenied, http.StatusForbidden},
		{"rate limited", auth.ErrRateLimited, CodeRateLimited, http.StatusTooManyRequests},
		{"wrapped not found", wrap(core.ErrNotFound), CodeFsNotFound, http.StatusNotFound},
		{"unhandled", errors.New("boom"), internalCode, http.StatusInternalServerError},
		{"nil is not an error", nil, "", http.StatusOK},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			status, err := Map(tt.err)
			if tt.err == nil {
				if status != http.StatusOK || err != nil {
					t.Fatalf("Map(nil) = (%d, %v), want (200, nil)", status, err)
				}
				return
			}
			if status != tt.want {
				t.Errorf("Map(%v) status = %d, want %d", tt.err, status, tt.want)
			}
			if err == nil {
				t.Fatalf("Map(%v) returned a nil error", tt.err)
			}
			if err.Code != tt.code {
				t.Errorf("Map(%v) code = %q, want %q", tt.err, err.Code, tt.code)
			}
		})
	}
}

func wrap(err error) error { return errors.Join(errors.New("context"), err) }

// A precondition refusal carries the current token so a conflict screen can
// show it without a second round trip.
func TestMapPreconditionCarriesToken(t *testing.T) {
	_, err := Map(&core.PreconditionError{Current: "w7"})
	got := err.Args
	if len(got) != 1 || got[0].Name != "current_etag" || got[0].Value != "w7" {
		t.Fatalf("precondition args = %v, want current_etag=w7", got)
	}
}

// A 413 names the limit and the numbers, so a caller can act on the refusal.
func TestMapLimitCarriesNumbers(t *testing.T) {
	_, err := Map(limits.Exceed("RequestBody", 1<<20, 1<<21))
	names := map[string]string{}
	for _, a := range err.Args {
		names[a.Name] = a.Value
	}
	if names["limit"] != "RequestBody" || names["bound"] != "1048576" || names["got"] != "2097152" {
		t.Fatalf("limit args = %v", names)
	}
}

// A 500 has no detail and no catalogue key, so nothing internal reaches the
// wire. Correlation is the Sc-Trace header, which is middleware, not body.
func TestMapInternalHasNoDetail(t *testing.T) {
	_, err := Map(errors.New("boom"))
	b, merr := json.Marshal(err)
	if merr != nil {
		t.Fatalf("marshal: %v", merr)
	}
	if bytes.Contains(b, []byte("detail")) {
		t.Fatalf("500 body leaks detail: %s", b)
	}
}

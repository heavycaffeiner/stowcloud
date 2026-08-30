//go:build linux

package dav

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/heavycaffeiner/stowcloud/go/engine/kit/limits"
	"github.com/heavycaffeiner/stowcloud/go/engine/service/core"
)

// Absence and denial answer alike, which is what stops a stranger mapping a
// tree they cannot read by watching which paths answer 403 and which 404.
func TestAbsenceAndDenialAreDistinguishableOnlyToSomeoneWhoMayLook(t *testing.T) {
	t.Parallel()

	missing, _ := StatusOf(core.ErrNotFound)
	if missing != http.StatusNotFound {
		t.Errorf("a missing resource answered %d", missing)
	}
	// Denial is 403 here because the caller was already resolved: the mount
	// answers 404 for a path outside every grant, before this is reached.
	denied, _ := StatusOf(core.ErrDenied)
	if denied != http.StatusForbidden {
		t.Errorf("a denial answered %d", denied)
	}
}

// A lock refusal is the one status carrying a precondition element. A client
// reads it to learn that resubmitting with the token is what would work.
func TestALockRefusalNamesItsPrecondition(t *testing.T) {
	t.Parallel()

	status, cond := StatusOf(ErrLocked)
	if status != http.StatusLocked {
		t.Errorf("a locked resource answered %d, want 423", status)
	}
	if cond.Local != "lock-token-submitted" {
		t.Errorf("the precondition is %q", cond.Local)
	}
	if cond.Space != davNS {
		t.Errorf("the precondition is in namespace %q", cond.Space)
	}
}

// Wrapping is preserved, since every caller wraps with context before
// returning. A mapping that only matched bare sentinels would answer 500 for
// every real failure.
func TestAWrappedSentinelStillMaps(t *testing.T) {
	t.Parallel()

	wrapped := fmt.Errorf("creating %s: %w", "notes.txt", core.ErrNoSpace)
	if got, _ := StatusOf(wrapped); got != http.StatusInsufficientStorage {
		t.Errorf("a wrapped out-of-space answered %d, want 507", got)
	}
}

// A failure nobody classified is 500 rather than something specific. Guessing
// would tell a client to retry what it should not.
func TestAnUnknownFailureIsInternal(t *testing.T) {
	t.Parallel()

	status, cond := StatusOf(errors.New("something nobody mapped"))
	if status != http.StatusInternalServerError {
		t.Errorf("an unmapped failure answered %d", status)
	}
	if cond.Local != "" {
		t.Errorf("an unmapped failure invented the precondition %q", cond.Local)
	}
}

// Nil is success, so a handler can map unconditionally without checking first.
func TestNilIsOK(t *testing.T) {
	t.Parallel()

	if got, _ := StatusOf(nil); got != http.StatusOK {
		t.Errorf("nil mapped to %d", got)
	}
}

// The whole table, by the distinction each entry is making.
func TestTheStatusTable(t *testing.T) {
	t.Parallel()

	for _, c := range []struct {
		name string
		err  error
		want int
		why  string
	}{
		{"a malformed body", ErrDirective, http.StatusBadRequest,
			"the request is at fault, not the server's state"},
		{"a body over its bound", ErrBodyTooLarge, http.StatusBadRequest,
			"the bound is on what a client may send, so it is not 507"},
		{"an If header that did not hold", ErrPreconditionFailed, http.StatusPreconditionFailed,
			"the client stated a condition and it was false"},
		{"a Destination on another host", ErrForeignDestination, http.StatusBadGateway,
			"the request is well formed and this server cannot serve it"},
		{"a PUT onto a collection", core.ErrExists, http.StatusMethodNotAllowed,
			"the target exists and the method cannot apply to what is there"},
		{"a body on a method defining none", ErrUnsupportedMedia, http.StatusUnsupportedMediaType,
			"MKCOL defines no body format"},
		{"a missing parent", core.ErrConflict, http.StatusConflict,
			"409 tells a client to create the parent; 404 gives it no reason to"},
		{"a collection with members", core.ErrNotEmpty, http.StatusConflict,
			"the tree is not what the request expected"},
		{"a move across shares", core.ErrCrossShare, http.StatusConflict,
			"no rename spans two shares atomically"},
		{"the volume is full", core.ErrNoSpace, http.StatusInsufficientStorage,
			"a client retries this differently from a refusal"},
		{"a quota exceeded", core.ErrQuotaExceeded, http.StatusInsufficientStorage,
			"the same, for a configured ceiling"},
		{"a bound exceeded", limits.ErrTooLarge, http.StatusInsufficientStorage,
			"more of a durable resource than the caller may have"},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			if got, _ := StatusOf(c.err); got != c.want {
				t.Errorf("answered %d, want %d: %s", got, c.want, c.why)
			}
		})
	}
}

// A share whose backing went away is 503, not 404.
//
// This one is worth its own case: a sync client reading 404 concludes the file
// was deleted and removes its local copy. An unreachable disk must not look
// like an intentional deletion.
func TestAnUnreachableShareIsNotReportedAsDeleted(t *testing.T) {
	t.Parallel()

	got, _ := StatusOf(core.ErrShareBroken)
	if got == http.StatusNotFound {
		t.Fatal("an unreachable share answered 404, which a sync client reads as a deletion")
	}
	if got != http.StatusServiceUnavailable {
		t.Errorf("an unreachable share answered %d, want 503", got)
	}
}

// An error with a precondition writes it as a parsable document, so a client
// can branch on the element rather than on the status alone.
func TestAnErrorBodyCarriesTheConditionElement(t *testing.T) {
	t.Parallel()

	w := httptest.NewRecorder()
	mustWriteError(t, w, ErrLocked)

	if w.Code != http.StatusLocked {
		t.Errorf("the response is %d, want 423", w.Code)
	}
	if ct := w.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/xml") {
		t.Errorf("the content type is %q", ct)
	}
	body := w.Body.String()
	if !strings.Contains(body, "<D:lock-token-submitted/>") {
		t.Errorf("the condition element is missing: %s", body)
	}
	if !strings.Contains(body, `xmlns:D="DAV:"`) {
		t.Errorf("the namespace is not declared: %s", body)
	}
}

// An error with no condition is a plain response. Inventing an element would
// have a client parse for a condition the specification does not define.
func TestAnErrorWithoutAConditionIsPlain(t *testing.T) {
	t.Parallel()

	w := httptest.NewRecorder()
	mustWriteError(t, w, core.ErrNotFound)

	if w.Code != http.StatusNotFound {
		t.Errorf("the response is %d, want 404", w.Code)
	}
	if strings.Contains(w.Body.String(), "<D:error") {
		t.Errorf("a condition-less failure wrote an error document: %s", w.Body.String())
	}
}

// An unmapped failure writes 500 and reveals nothing about what happened.
func TestAnInternalErrorBodySaysNothingSpecific(t *testing.T) {
	t.Parallel()

	w := httptest.NewRecorder()
	mustWriteError(t, w, errors.New("the database is at /var/lib/secret.db"))

	if w.Code != http.StatusInternalServerError {
		t.Errorf("the response is %d, want 500", w.Code)
	}
	if strings.Contains(w.Body.String(), "secret.db") {
		t.Errorf("the internal detail reached the client: %s", w.Body.String())
	}
}

// mustWriteError writes and fails the test if the body did not go out.
func mustWriteError(t *testing.T, w http.ResponseWriter, err error) {
	t.Helper()
	if werr := WriteError(w, err); werr != nil {
		t.Fatalf("writing the error response: %v", werr)
	}
}

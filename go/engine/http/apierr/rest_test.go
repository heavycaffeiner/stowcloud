package apierr

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/heavycaffeiner/stowcloud/go/engine/service/auth"
	"github.com/heavycaffeiner/stowcloud/go/engine/service/core"
	"github.com/heavycaffeiner/stowcloud/go/engine/service/preview"
	"github.com/heavycaffeiner/stowcloud/go/engine/service/upload"
)

// The existence rule, which is the reason this package has a visibility input.
//
// A caller who may not see a file and a caller asking about a file that is not
// there get the same answer: same status, same code, same message, same bytes.
// Anything less makes the 403 a confirmation that the resource exists, which is
// the whole thing the rule prevents.
//
// Byte-exact rather than field-by-field, because a difference in the encoded
// form is still a difference a client can measure.
func TestTheHiddenResponsesAreByteIdentical(t *testing.T) {
	var bodies []string
	var statuses []int

	for _, err := range []error{
		core.ErrNotFound,
		core.ErrDenied,
		fmt.Errorf("wrapped: %w", core.ErrDenied),
		fmt.Errorf("resolving %q: %w", "a/b", core.ErrNotFound),
	} {
		status, body := REST(Classify(err, VisibilityHidden))
		raw, merr := json.Marshal(body)
		if merr != nil {
			t.Fatalf("encoding: %v", merr)
		}
		statuses = append(statuses, status)
		bodies = append(bodies, string(raw))
	}

	for i := range bodies {
		if bodies[i] != bodies[0] {
			t.Errorf("hidden response %d differs:\n  %s\n  %s", i, bodies[0], bodies[i])
		}
		if statuses[i] != statuses[0] {
			t.Errorf("hidden response %d has status %d, want %d", i, statuses[i], statuses[0])
		}
	}
	if statuses[0] != http.StatusNotFound {
		t.Errorf("the hidden status is %d, want 404", statuses[0])
	}
	if !strings.Contains(bodies[0], "fs.not_found") {
		t.Errorf("the hidden body does not carry the not-found code: %s", bodies[0])
	}
}

// The other half: where the caller already learned the resource exists, a
// denial is reported as one. Without this the fold would make every denial
// unreportable, including on surfaces where saying so is correct.
func TestAKnownDenialIsReportedAsDenied(t *testing.T) {
	status, body := REST(Classify(core.ErrDenied, VisibilityKnown))
	if status != http.StatusForbidden {
		t.Errorf("a known denial answered %d, want 403", status)
	}
	if body.Code != "fs.denied" {
		t.Errorf("the code is %q", body.Code)
	}
}

// A missing resource stays 404 under either visibility: the fold is about
// hiding a denial, not about changing what absence means.
func TestAbsenceIsNotFoundUnderEitherVisibility(t *testing.T) {
	for _, v := range []Visibility{VisibilityHidden, VisibilityKnown} {
		status, body := REST(Classify(core.ErrNotFound, v))
		if status != http.StatusNotFound {
			t.Errorf("absence under %v answered %d", v, status)
		}
		if body.Code != "fs.not_found" {
			t.Errorf("absence under %v answered the code %q", v, body.Code)
		}
	}
}

// An unrecognised error is Internal rather than a guess, because a guessed
// class produces a status that tells the caller what was guessed.
func TestAnUnrecognisedErrorIsInternal(t *testing.T) {
	status, body := REST(Classify(errors.New("something nobody mapped"), VisibilityKnown))
	if status != http.StatusInternalServerError {
		t.Errorf("an unmapped error answered %d, want 500", status)
	}
	if body.Code != codeInternal {
		t.Errorf("the code is %q", body.Code)
	}
}

// An internal error carries no detail, in the struct and in the encoding.
//
// What went wrong inside is not the caller's to read, and a catalogue key
// naming the internal condition describes the fault by another route.
func TestAnInternalErrorCarriesNoDetail(t *testing.T) {
	_, body := REST(Classified{Class: Internal, Key: "some.internal.condition",
		Args: []Arg{{Name: "table", Value: "share_grant"}}})

	if body.Key != "" {
		t.Errorf("the struct carries the key %q", body.Key)
	}
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	for _, leak := range []string{"detail", "reason_key", "some.internal.condition", "share_grant"} {
		if strings.Contains(string(raw), leak) {
			t.Errorf("the encoded internal error carries %q: %s", leak, raw)
		}
	}
}

// A classified error does carry its detail, or the client has nothing to
// render and the catalogue is pointless.
func TestAClassifiedErrorCarriesItsDetail(t *testing.T) {
	_, body := REST(Classified{Class: Unprocessable, Key: "fs.invalid_name",
		Args: []Arg{{Name: "component", Value: "name"}}})

	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"fs.invalid_name", "reason_params", "component"} {
		if !strings.Contains(string(raw), want) {
			t.Errorf("the encoded error is missing %q: %s", want, raw)
		}
	}
}

// Every class renders, so a class added without a table row fails here rather
// than answering with status 0 and an empty code.
func TestEveryClassHasARendering(t *testing.T) {
	table := restTable()
	for c := Internal; c <= FlowTooSoon; c++ {
		entry, ok := table[c]
		if !ok {
			t.Errorf("the class %s has no REST rendering", c)
			continue
		}
		if entry.status < 200 || entry.status > 599 {
			t.Errorf("the class %s renders status %d", c, entry.status)
		}
		if entry.code == "" || entry.message == "" {
			t.Errorf("the class %s renders code %q message %q", c, entry.code, entry.message)
		}
	}
}

// Every class prints as a name, because classes appear in logs and in the
// failure text above.
func TestEveryClassHasAName(t *testing.T) {
	for c := Internal; c <= FlowTooSoon; c++ {
		name := c.String()
		if name == "" || strings.HasPrefix(name, "class(") {
			t.Errorf("the class %d has no name, printing as %q", c, name)
		}
	}
}

// A request error carries a semantic class rather than a status, so the same
// value renders correctly on any surface. The old tree stored the status, which
// is what made those errors unusable outside REST.
func TestRequestErrorsCarryAClassNotAStatus(t *testing.T) {
	for _, c := range []struct {
		err        error
		wantStatus int
	}{
		{BadRequest("fs.bad_json", "path"), http.StatusBadRequest},
		{UnprocessableInput("fs.invalid_name", "name"), http.StatusUnprocessableEntity},
		{BadGatewayError("dav.foreign_destination", "destination"), http.StatusBadGateway},
	} {
		status, body := REST(Classify(c.err, VisibilityKnown))
		if status != c.wantStatus {
			t.Errorf("%v answered %d, want %d", c.err, status, c.wantStatus)
		}
		if len(body.Args) == 0 {
			t.Errorf("%v carried no argument, so a client cannot point at the input", c.err)
		}
	}
}

// A request error names the field and never its value: echoing what the client
// sent turns a refusal into a reflection.
func TestARequestErrorNamesTheFieldNotItsValue(t *testing.T) {
	err := BadRequest("fs.bad_json", "path")
	_, body := REST(Classify(err, VisibilityKnown))

	var found bool
	for _, a := range body.Args {
		if a.Name == "field" && a.Value == "path" {
			found = true
		}
	}
	if !found {
		t.Errorf("the field name is not carried: %+v", body.Args)
	}

	raw, merr := json.Marshal(body)
	if merr != nil {
		t.Fatal(merr)
	}
	// The field's name is a constant this code chose; a value would have come
	// from the request.
	if strings.Contains(string(raw), "../../etc/passwd") {
		t.Errorf("a client-supplied value reached the envelope: %s", raw)
	}
}

// A parser reporting what it found travels through unchanged, so a protocol
// package never needs a second sentinel ladder.
func TestAPreClassifiedErrorTravelsThrough(t *testing.T) {
	err := AsClassified(Locked, "dav.locked", Arg{Name: "token", Value: "opaque"})
	status, body := REST(Classify(err, VisibilityKnown))

	if status != http.StatusLocked {
		t.Errorf("a pre-classified lock answered %d, want 423", status)
	}
	if body.Key != "dav.locked" {
		t.Errorf("the key is %q", body.Key)
	}
}

// The classes the sentinels map to, spot-checked across all four services, so
// the table is exercised rather than only counted.
func TestTheSentinelsClassifyAsSpecified(t *testing.T) {
	for _, c := range []struct {
		err        error
		wantStatus int
		wantCode   string
	}{
		{core.ErrExists, http.StatusConflict, "fs.exists"},
		{core.ErrNotEmpty, http.StatusConflict, "fs.not_empty"},
		{core.ErrNoSpace, http.StatusInsufficientStorage, "fs.no_space"},
		// The code comes from the class, so a quota and a full disk share it.
		// The catalogue key is what distinguishes them, and the next test
		// checks that survives.
		{core.ErrQuotaExceeded, http.StatusInsufficientStorage, "fs.no_space"},
		{core.ErrLinkExpired, http.StatusGone, "link.expired"},
		{core.ErrShareBroken, http.StatusServiceUnavailable, "fs.share_unavailable"},

		{auth.ErrRateLimited, http.StatusTooManyRequests, "auth.rate_limited"},
		{auth.ErrCredentials, http.StatusUnauthorized, "auth.invalid_credentials"},
		{auth.ErrAccountDisabled, http.StatusForbidden, "auth.account_disabled"},
		{auth.ErrLastAdmin, http.StatusConflict, "admin.last_admin"},
		{auth.ErrWeakPassword, http.StatusUnprocessableEntity, "auth.weak_password"},
		{auth.ErrNameTaken, http.StatusConflict, "admin.name_taken"},

		{upload.ErrTooLarge, http.StatusRequestEntityTooLarge, "http.body_too_large"},
		{upload.ErrChecksum, http.StatusUnprocessableEntity, "unprocessable"},
		{upload.ErrSessionExpired, http.StatusGone, "link.expired"},

		{preview.ErrUnsupported, http.StatusNotImplemented, "not_implemented"},
		{preview.ErrWorkerBusy, http.StatusServiceUnavailable, "subsystem.unavailable"},
	} {
		status, body := REST(Classify(c.err, VisibilityKnown))
		if status != c.wantStatus || body.Code != c.wantCode {
			t.Errorf("%v answered %d %q, want %d %q", c.err, status, body.Code, c.wantStatus, c.wantCode)
		}
	}
}

// The class assigns the code and the sentinel assigns the key, so two errors
// sharing a class stay distinguishable to a client that renders the catalogue.
//
// A quota and a full filesystem are both 507 and both fs.no_space, and a user
// hitting a quota needs to be told something different from one whose disk is
// full: the first is an administrator conversation and the second is not.
func TestTwoSentinelsSharingAClassKeepTheirKeys(t *testing.T) {
	_, quota := REST(Classify(core.ErrQuotaExceeded, VisibilityKnown))
	_, disk := REST(Classify(core.ErrNoSpace, VisibilityKnown))

	if quota.Code != disk.Code {
		t.Errorf("the shared class produced two codes: %q and %q", quota.Code, disk.Code)
	}
	if quota.Key == disk.Key {
		t.Errorf("both render the key %q, so a client cannot tell a quota from a full disk", quota.Key)
	}
	if quota.Key != "fs.quota_exceeded" || disk.Key != "fs.no_space" {
		t.Errorf("the keys are %q and %q", quota.Key, disk.Key)
	}
}

// A second factor is not a refusal: the password verified and the code screen
// is next. Reporting it as invalid credentials leaves an enrolled account
// unable to present its code.
func TestASecondFactorIsNotAnInvalidCredential(t *testing.T) {
	_, body := REST(Classify(auth.ErrSecondFactor, VisibilityKnown))
	if body.Code == "auth.invalid_credentials" {
		t.Error("a second-factor prompt reports as invalid credentials")
	}
	if body.Key != "auth.totp_required" {
		t.Errorf("the key is %q, so the screen cannot ask for the code", body.Key)
	}
}

// Write renders to a response with the JSON content type and the mapped status.
func TestWriteRendersTheEnvelope(t *testing.T) {
	w := httptest.NewRecorder()
	Write(w, core.ErrNotFound, VisibilityHidden)

	if w.Code != http.StatusNotFound {
		t.Errorf("Write answered %d", w.Code)
	}
	if ct := w.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("the content type is %q", ct)
	}
	var out map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("the body is not JSON: %v\n%s", err, w.Body)
	}
	if _, ok := out["error"]; !ok {
		t.Errorf("the body has no error object: %s", w.Body)
	}
}

// A batch item's outcome carries the same code and key a whole-response error
// would, so a client renders both alike.
func TestABatchItemRendersLikeAResponse(t *testing.T) {
	item := WireOf(core.ErrExists, VisibilityKnown)
	_, body := REST(Classify(core.ErrExists, VisibilityKnown))

	if item.Code != body.Code {
		t.Errorf("the batch item's code is %q and the response's is %q", item.Code, body.Code)
	}
	if item.Key != body.Key {
		t.Errorf("the batch item's key is %q and the response's is %q", item.Key, body.Key)
	}
}

// A batch item for a hidden error is the same as the response's, so a batch
// cannot become the surface that reveals what the single request hid.
func TestABatchItemHonoursTheExistenceRule(t *testing.T) {
	denied := WireOf(core.ErrDenied, VisibilityHidden)
	missing := WireOf(core.ErrNotFound, VisibilityHidden)

	// Compared as encoded bytes, which is what a client actually receives, and
	// which is also the only comparison available: the item carries a map.
	a, aerr := json.Marshal(denied)
	b, berr := json.Marshal(missing)
	if aerr != nil || berr != nil {
		t.Fatalf("encoding: %v %v", aerr, berr)
	}
	if string(a) != string(b) {
		t.Errorf("a batch distinguishes denied from missing:\n  %s\n  %s", a, b)
	}
}

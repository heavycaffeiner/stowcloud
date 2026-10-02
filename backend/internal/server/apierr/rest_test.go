// Linux only, because it classifies errors from services that are Linux only.
//go:build linux

package apierr

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"
)

// testClassifier stands in for the product table with the two sentinels the
// existence rule is about.
func testClassifier() (k *Classifier, denied, missing error) {
	denied = errors.New("denied")
	missing = errors.New("missing")
	return NewClassifier([]Sentinel{
		{Err: denied, Class: Denied, Key: "fs.denied"},
		{Err: missing, Class: NotFound, Key: "fs.not_found"},
	}), denied, missing
}

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
	k, denied, missing := testClassifier()
	var bodies []string
	var statuses []int

	for _, err := range []error{
		missing,
		denied,
		fmt.Errorf("wrapped: %w", denied),
		fmt.Errorf("resolving %q: %w", "a/b", missing),
	} {
		status, body := REST(k.Classify(err, VisibilityHidden))
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

// An unrecognised error is Internal rather than a guess, because a guessed
// class produces a status that tells the caller what was guessed.
func TestAnUnrecognisedErrorIsInternal(t *testing.T) {
	status, body := REST(NewClassifier(nil).Classify(errors.New("something nobody mapped"), VisibilityKnown))
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

// A request error names the field and never its value: echoing what the client
// sent turns a refusal into a reflection.
func TestARequestErrorNamesTheFieldNotItsValue(t *testing.T) {
	err := BadRequest("fs.bad_json", "path")
	_, body := REST(NewClassifier(nil).Classify(err, VisibilityKnown))

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

// A batch item for a hidden error is the same as the response's, so a batch
// cannot become the surface that reveals what the single request hid.
func TestABatchItemHonoursTheExistenceRule(t *testing.T) {
	k, deniedErr, missingErr := testClassifier()
	denied := k.WireOf(deniedErr, VisibilityHidden)
	missing := k.WireOf(missingErr, VisibilityHidden)

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

//go:build linux

package lifecycle

import (
	"os"
	"regexp"
	"strconv"
	"testing"

	"github.com/heavycaffeiner/stowcloud/go/engine/http/server"
)

// The cutover document's route counts match the tree.
//
// Those numbers are what a decision about deleting the old tree gets made
// against, and a document that drifts is worse than none: it reads as
// current. One commit message already carries a count that is wrong by three,
// which is what this exists to stop happening again.
func cutoverDocPath() string {
	if _, err := os.Stat("../../../docs/internal/refactor/cutover.md"); err == nil {
		return "../../../docs/internal/refactor/cutover.md"
	}
	return "../../../docs/refactor/cutover.md"
}

func TestTheCutoverDocumentMatchesTheTree(t *testing.T) {
	doc, err := os.ReadFile(cutoverDocPath())
	if err != nil {
		t.Fatalf("reading the cutover document: %v", err)
	}
	table := len(server.Table())
	bound := boundCount(t)

	claimed := numbersIn(t, doc, `binds (\d+) of the (\d+) routes`)
	if claimed[0] != bound {
		t.Errorf("the document says %d routes are bound; the tree binds %d",
			claimed[0], bound)
	}
	if claimed[1] != table {
		t.Errorf("the document says the table holds %d routes; it holds %d",
			claimed[1], table)
	}

	// The list of unbound routes and what it has to agree with are checked by
	// TestTheDocumentsUnboundListIsAccurate, which also holds the case this one
	// cannot express: a list that is empty because there is nothing left to
	// name, rather than because the section was deleted.
}

// boundCount counts the names the binding switch handles.
//
// Read from the source because every name in the handler map is bound to
// something: the ones with no service get the fallback, and a map lookup
// cannot tell those apart from a real binding.
func boundCount(t *testing.T) int {
	t.Helper()

	src, err := os.ReadFile("mount.go")
	if err != nil {
		t.Fatalf("reading mount.go: %v", err)
	}
	found := regexp.MustCompile(`case "[a-z0-9.-]+":`).FindAll(src, -1)
	if len(found) == 0 {
		t.Fatal("mount.go has no bindings; if the switch was rewritten, this check has stopped watching anything")
	}
	return len(found)
}

// namedRoutes collects the route names the document's unbound list carries.
func namedRoutes(doc []byte) map[string]struct{} {
	section := regexp.MustCompile(`(?s)waits on:\n\n(.*?)\n\n###`).FindSubmatch(doc)
	if section == nil {
		return nil
	}

	out := map[string]struct{}{}
	// A route name is usually dotted, and one is not: `events`. A pattern
	// that required a dot silently dropped it and reported the list one short,
	// which is the same class of error this file exists to catch.
	for _, m := range regexp.MustCompile("`([a-z][a-z0-9-]*(?:\\.[a-z0-9-]+)*)`").FindAllSubmatch(section[1], -1) {
		out[string(m[1])] = struct{}{}
	}
	return out
}

// Every route the document names as unbound is one the tree leaves unbound.
//
// A list that named a bound route, or a name that does not exist, would read
// as work still to do when it is already done or was never there.
func TestTheDocumentsUnboundListIsAccurate(t *testing.T) {
	doc, err := os.ReadFile(cutoverDocPath())
	if err != nil {
		t.Fatalf("reading the cutover document: %v", err)
	}

	src, err := os.ReadFile("mount.go")
	if err != nil {
		t.Fatalf("reading mount.go: %v", err)
	}
	boundNames := map[string]struct{}{}
	for _, m := range regexp.MustCompile(`case "([a-z0-9.-]+)":`).FindAllSubmatch(src, -1) {
		boundNames[string(m[1])] = struct{}{}
	}

	real := map[string]struct{}{}
	for _, r := range server.Table() {
		real[r.Name] = struct{}{}
	}

	table := len(server.Table())
	unbound := table - boundCount(t)

	named := namedRoutes(doc)
	if len(named) != unbound {
		t.Errorf("the document lists %d unbound routes; %d are unbound", len(named), unbound)
	}
	if unbound == 0 {
		// Every route is bound, so there is no list to check and an empty one
		// is the correct document. Asserting the count above is what keeps
		// this from passing vacuously: a route that loses its binding puts a
		// number back here and fails until the document says so.
		return
	}
	for name := range named {
		if _, exists := real[name]; !exists {
			t.Errorf("the document names %q, which is not in the route table", name)
		}
		if _, isBound := boundNames[name]; isBound {
			t.Errorf("the document lists %q as unbound; it is bound", name)
		}
	}
}

// numbersIn pulls the capture groups of the first match.
func numbersIn(t *testing.T, doc []byte, pattern string) []int {
	t.Helper()

	m := regexp.MustCompile(pattern).FindSubmatch(doc)
	if m == nil {
		t.Fatalf("the document has no line matching %q, so this check is watching nothing", pattern)
	}

	out := make([]int, 0, len(m)-1)
	for _, group := range m[1:] {
		n, err := strconv.Atoi(string(group))
		if err != nil {
			t.Fatalf("%q is not a number: %v", group, err)
		}
		out = append(out, n)
	}
	return out
}

//go:build linux

package agent

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// The agent keeps its own copy of the account-name rule, and this holds the
// copy to the shared vectors.
//
// The duplication is deliberate. Auth gates creation, so a server predating
// that rule may hold names it would now refuse, and this is the last check
// before a name enters the system account file and the credential tool's
// argument list.
//
// Auth carries the identical vector file and runs the identical assertion
// against its own rule, so a change to either implementation that is not a
// change to both fails on one side.

// Vector is one shared account-name case: the name and whether the one rule
// admits it.
type Vector struct {
	Name  string
	Valid bool
}

// readVectors parses the shared vector file.
//
// Both this package and auth carry a copy, and a test below asserts the two
// copies are byte identical. Copying rather than importing is what the layer
// rule leaves available: the packages sit in the same tier and may not import
// each other, so the parity travels as data.
func readVectors(t *testing.T, path string) []Vector {
	t.Helper()
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	var out []Vector
	for n, line := range strings.Split(string(body), "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		verdict, quoted, ok := strings.Cut(trimmed, "\t")
		if !ok {
			t.Fatalf("%s:%d: no tab between the verdict and the name", path, n+1)
		}
		name, uerr := strconv.Unquote(quoted)
		if uerr != nil {
			t.Fatalf("%s:%d: the name is not a quoted string: %v", path, n+1, uerr)
		}
		switch verdict {
		case "valid":
			out = append(out, Vector{Name: name, Valid: true})
		case "invalid":
			out = append(out, Vector{Name: name})
		default:
			t.Fatalf("%s:%d: the verdict is %q, want valid or invalid", path, n+1, verdict)
		}
	}
	if len(out) == 0 {
		t.Fatalf("%s holds no vectors, so a parity test over it would pass without checking anything", path)
	}
	return out
}

func TestValidNameAgreesWithTheSharedVectors(t *testing.T) {
	for _, v := range readVectors(t, filepath.Join("testdata", "usernames.txt")) {
		if got := ValidName(v.Name); got != v.Valid {
			t.Errorf("ValidName(%q) = %v, the shared vectors say %v", v.Name, got, v.Valid)
		}
	}
}

// The two copies of the vector file have to stay identical, or each side is
// proving itself against its own idea of the rule.
func TestTheVectorFileMatchesAuths(t *testing.T) {
	mine, err := os.ReadFile(filepath.Join("testdata", "usernames.txt"))
	if err != nil {
		t.Fatal(err)
	}
	theirs, terr := os.ReadFile(filepath.Join("..", "..", "auth", "testdata", "usernames.txt"))
	if terr != nil {
		t.Skipf("auth's copy is not present in this checkout: %v", terr)
	}
	if string(mine) != string(theirs) {
		t.Error("the two vector files have drifted; the parity they prove is no longer one rule")
	}
}

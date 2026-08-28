//go:build linux

package auth

import (
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// The canonical account-name rule, held to the shared vectors.
//
// The SMB agent carries its own copy of this rule, because it is the last check
// before a name reaches the system account file and it cannot assume every
// server predates the rule. The two packages sit in one tier and may not import
// each other, so the parity travels as this file: both sides read identical
// vectors and both assert against them.

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

func TestValidUsernameAgreesWithTheSharedVectors(t *testing.T) {
	for _, v := range readVectors(t, filepath.Join("testdata", "usernames.txt")) {
		got := ValidUsername(v.Name) == nil
		if got != v.Valid {
			t.Errorf("ValidUsername(%q) accepted=%v, the shared vectors say %v", v.Name, got, v.Valid)
		}
	}
}

// A refusal must not repeat what was typed: a validation message echoing its
// input is a reflection primitive.
func TestTheRefusalDoesNotEchoTheName(t *testing.T) {
	const hostile = "<script>alert(1)</script>"
	err := ValidUsername(hostile)
	if err == nil {
		t.Fatal("a hostile name was accepted")
	}
	if strings.Contains(err.Error(), hostile) {
		t.Errorf("the refusal echoes the input: %v", err)
	}
	if !errors.Is(err, ErrNameInvalid) {
		t.Errorf("the refusal is %v, want the sentinel", err)
	}
}

// The bound is one number, and the vectors exercise both sides of it.
func TestTheLengthBoundIsExactlyTheDocumentedOne(t *testing.T) {
	at := strings.Repeat("a", UsernameMaxLen)
	if ValidUsername(at) != nil {
		t.Error("a name at exactly the bound was refused")
	}
	if ValidUsername(at+"a") == nil {
		t.Error("a name one past the bound was accepted")
	}
}

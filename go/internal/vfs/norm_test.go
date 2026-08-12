package vfs

import (
	"slices"
	"testing"
)

const (
	// The same user-visible name in the two forms a client may have written:
	// precomposed, and "e" followed by a combining acute accent. Spelled with
	// escapes so the difference survives an editor that normalises what it
	// saves, which is the failure this package exists to absorb.
	nfcName = "café"
	nfdName = "café"
)

func TestTheTwoSpellingsActuallyDiffer(t *testing.T) {
	if nfcName == nfdName {
		t.Fatal("the fixtures collapsed to one spelling and every test below is vacuous")
	}
}

func TestLookupCandidatesTryTheBytesGivenFirst(t *testing.T) {
	got := lookupCandidates(nfdName)
	if got[0] != nfdName {
		t.Fatalf("the exact bytes have to be tried first, got %q", got[0])
	}
	if !slices.Contains(got, nfcName) {
		t.Fatalf("NFC missing from %q", got)
	}
}

func TestLookupCandidatesDeduplicate(t *testing.T) {
	got := lookupCandidates("plain-ascii")
	if len(got) != 1 {
		t.Fatalf("an ASCII name has one spelling, got %v", got)
	}
}

func TestPathCandidatesApplyOneFormAcrossEveryComponent(t *testing.T) {
	got := pathCandidates([]string{nfdName, nfdName})
	if len(got) != 2 {
		t.Fatalf("want the bytes given plus NFC, got %v", got)
	}
	if got[0] != nfdName+"/"+nfdName {
		t.Fatalf("candidate 0 = %q", got[0])
	}
	if got[1] != nfcName+"/"+nfcName {
		t.Fatalf("candidate 1 = %q", got[1])
	}
}

func TestPathCandidatesForTheRoot(t *testing.T) {
	got := pathCandidates(nil)
	if len(got) != 1 || got[0] != "." {
		t.Fatalf(`the root resolves through ".", got %v`, got)
	}
}

func TestNormalizeNewName(t *testing.T) {
	if got := normalizeNewName(nfdName); got != nfcName {
		t.Fatalf("a new name is created in NFC, got %q", got)
	}
}

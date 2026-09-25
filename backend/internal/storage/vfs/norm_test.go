package vfs

import (
	"slices"
	"testing"
)

// The same user-visible name in two forms: precomposed ("e" with an acute
// accent as one code point) and decomposed ("e" plus a combining acute
// accent). Written with the literal bytes so an editor that normalizes on
// save cannot collapse the difference this test depends on.
const (
	nfcSpelling = "caf\u00e9"
	nfdSpelling = "cafe\u0301"
)

func TestFixturesActuallyDiffer(t *testing.T) {
	if nfcSpelling == nfdSpelling {
		t.Fatal("the two fixture spellings collapsed to one; every test below is vacuous")
	}
}

func TestLookupCandidatesTriesTheGivenBytesFirst(t *testing.T) {
	got := lookupCandidates(nfdSpelling)
	if got[0] != nfdSpelling {
		t.Fatalf("candidate 0 = %q, want the exact bytes given", got[0])
	}
	if !slices.Contains(got, nfcSpelling) {
		t.Fatalf("NFC missing from %v", got)
	}
	if !slices.Contains(got, nfdSpelling) {
		t.Fatalf("NFD missing from %v", got)
	}
}

func TestLookupCandidatesFromNFCStillReachesNFD(t *testing.T) {
	got := lookupCandidates(nfcSpelling)
	if got[0] != nfcSpelling {
		t.Fatalf("candidate 0 = %q, want the exact bytes given", got[0])
	}
	if !slices.Contains(got, nfdSpelling) {
		t.Fatalf("NFD missing from %v", got)
	}
}

func TestLookupCandidatesDeduplicateForASCII(t *testing.T) {
	got := lookupCandidates("plain-ascii")
	if len(got) != 1 {
		t.Fatalf("an ASCII name has one spelling in every normal form, got %v", got)
	}
}

func TestPathCandidatesApplyOneFormAcrossEveryComponent(t *testing.T) {
	got := pathCandidates([]string{nfdSpelling, nfdSpelling})
	if len(got) != 2 {
		t.Fatalf("want exactly the given spelling and its NFC form, got %v", got)
	}
	if got[0] != nfdSpelling+"/"+nfdSpelling {
		t.Fatalf("candidate 0 = %q", got[0])
	}
	if got[1] != nfcSpelling+"/"+nfcSpelling {
		t.Fatalf("candidate 1 = %q", got[1])
	}
}

func TestPathCandidatesAtTheRoot(t *testing.T) {
	got := pathCandidates(nil)
	if len(got) != 1 || got[0] != "." {
		t.Fatalf(`pathCandidates(nil) = %v, want [.]`, got)
	}
}

func TestNormalizeNewNamePutsANewNameInNFC(t *testing.T) {
	if got := normalizeNewName(nfdSpelling); got != nfcSpelling {
		t.Fatalf("normalizeNewName(%q) = %q, want %q", nfdSpelling, got, nfcSpelling)
	}
	if got := normalizeNewName("plain-ascii"); got != "plain-ascii" {
		t.Fatalf("normalizeNewName should leave ASCII untouched, got %q", got)
	}
}

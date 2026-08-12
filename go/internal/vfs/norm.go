package vfs

import (
	"slices"
	"strings"

	"golang.org/x/text/unicode/norm"
)

// normalizeNewName puts a brand new on-disk name in NFC.
//
// Only a new name. An existing one is never rewritten: a name another program
// wrote is found as it is written, and renaming it to suit us breaks whatever
// external index recorded the old spelling.
func normalizeNewName(name string) string { return norm.NFC.String(name) }

// lookupCandidates are the spellings to try, in priority order, for a name that
// already exists on disk: the bytes given, then NFC, then NFD. macOS SMB
// clients create NFD names, so the same user-visible name has to be found
// either way. Deduplicated and order-preserving, so the common all-ASCII case
// costs one candidate.
func lookupCandidates(name string) []string {
	out := make([]string, 0, 3)
	out = append(out, name)
	if nfc := norm.NFC.String(name); !slices.Contains(out, nfc) {
		out = append(out, nfc)
	}
	if nfd := norm.NFD.String(name); !slices.Contains(out, nfd) {
		out = append(out, nfd)
	}
	return out
}

// pathCandidates does the same for a whole relative path, which the resolver
// hands to one openat2 call.
//
// Each candidate applies one normal form uniformly across every component:
// three candidates rather than three to the power of the depth, and correct for
// the case that actually happens, which is one non-conforming client writing a
// whole path in one form. A path whose components come from writers using
// different forms falls outside the approximation, and that gap is accepted
// rather than paid for on every resolution.
//
// The root yields ".", the relative path openat2 reads as "this directory".
func pathCandidates(comps []string) []string {
	if len(comps) == 0 {
		return []string{"."}
	}
	joined := func(f func(string) string) string {
		parts := make([]string, len(comps))
		for i, c := range comps {
			parts[i] = f(c)
		}
		return strings.Join(parts, "/")
	}
	out := make([]string, 0, 3)
	out = append(out, joined(func(s string) string { return s }))
	if nfc := joined(norm.NFC.String); !slices.Contains(out, nfc) {
		out = append(out, nfc)
	}
	if nfd := joined(norm.NFD.String); !slices.Contains(out, nfd) {
		out = append(out, nfd)
	}
	return out
}

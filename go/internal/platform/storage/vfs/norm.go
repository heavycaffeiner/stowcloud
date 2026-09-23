package vfs

import (
	"slices"
	"strings"

	"golang.org/x/text/unicode/norm"
)

// normalizeNewName puts a name this package is about to create into NFC.
//
// It is never applied to a name that already exists: a file another program
// wrote is found spelled as it wrote it, and silently renaming it to a
// different normal form would break any external index (an SMB client's own
// cache, a sync engine's own database) still keyed on the original bytes.
func normalizeNewName(name string) string { return norm.NFC.String(name) }

// lookupCandidates lists the spellings worth trying, in order, for a name
// that might already be on disk: exactly the bytes given, then NFC, then
// NFD. macOS writes filenames in NFD over SMB and AFP, so a lookup spelled
// in NFC by whatever is asking still has to find what macOS wrote.
//
// The result is deduplicated and order-preserving, so the ordinary all-ASCII
// name (where all three forms coincide) costs exactly one candidate.
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

// pathCandidates is lookupCandidates applied across a whole component chain
// in one pass, for a resolver that hands the whole chain to a single
// syscall.
//
// Each candidate applies one normal form uniformly to every component,
// producing three candidates total rather than three raised to the depth.
// This is a deliberate approximation: a path whose components were written
// by different tools in different normal forms falls outside it. That gap
// is accepted because the case that actually happens is one nonconforming
// client writing an entire tree in one form, not a tree with components
// mixed between forms.
//
// The root has no components and resolves through ".".
func pathCandidates(comps []string) []string {
	if len(comps) == 0 {
		return []string{"."}
	}
	join := func(convert func(string) string) string {
		parts := make([]string, len(comps))
		for i, c := range comps {
			parts[i] = convert(c)
		}
		return strings.Join(parts, "/")
	}
	asGiven := func(s string) string { return s }
	out := make([]string, 0, 3)
	out = append(out, join(asGiven))
	if nfc := join(norm.NFC.String); !slices.Contains(out, nfc) {
		out = append(out, nfc)
	}
	if nfd := join(norm.NFD.String); !slices.Contains(out, nfd) {
		out = append(out, nfd)
	}
	return out
}

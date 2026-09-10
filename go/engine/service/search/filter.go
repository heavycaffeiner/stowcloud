//go:build linux

package search

import "strings"

// What a search reports, as opposed to what it walks.
//
// A filter never prunes the traversal: a folder is descended into even when
// only files are wanted, because the files are underneath it. It decides one
// thing, per entry, after the permission check and the name match.

// Kind narrows a search to one sort of entry.
type Kind uint8

const (
	// KindAny reports files and folders alike.
	KindAny Kind = iota
	KindFile
	KindDir
)

// ParseKind reads the wire spelling of a kind.
//
// An unknown word is refused rather than widened back to everything. A client
// that asked for folders and received files has no way to tell its filter was
// dropped, and would present the whole tree as the answer to a narrow question.
func ParseKind(s string) (Kind, bool) {
	switch s {
	case "", "any":
		return KindAny, true
	case "file":
		return KindFile, true
	case "dir":
		return KindDir, true
	}
	return KindAny, false
}

// String is the wire spelling, so a round trip through the parser is one
// vocabulary rather than two.
func (k Kind) String() string {
	switch k {
	case KindFile:
		return "file"
	case KindDir:
		return "dir"
	default:
		return "any"
	}
}

// Filter narrows a result set.
//
// The zero value admits everything, which is what a caller that states no
// filter gets.
type Filter struct {
	Kind Kind
	// Exts holds extensions without the leading dot, already folded. Empty
	// admits every name. Build one with ParseExts rather than by hand: the
	// comparison here is byte equality against a folded name.
	Exts []string
}

// Admits reports whether one entry belongs in the answer.
//
// An extension filter never admits a folder. Folders carry no extension, and a
// directory named "photos.jpg" answering "show me the JPEGs" is a folder
// offered as a picture.
func (f Filter) Admits(name string, isDir bool) bool {
	switch f.Kind {
	case KindFile:
		if isDir {
			return false
		}
	case KindDir:
		if !isDir {
			return false
		}
	case KindAny:
	}

	if len(f.Exts) == 0 {
		return true
	}
	if isDir {
		return false
	}
	ext := ExtensionOf(name)
	if ext == "" {
		return false
	}
	for _, want := range f.Exts {
		if want == ext {
			return true
		}
	}
	return false
}

// Any reports whether the filter narrows anything, letting a hot loop skip the
// call entirely.
func (f Filter) Any() bool { return f.Kind != KindAny || len(f.Exts) > 0 }

// ExtensionOf is the folded extension of a name, without the dot.
//
// Empty when there is none. A leading dot is a dotfile's whole name and not an
// extension, so ".bashrc" has none, and a trailing dot names nothing either.
func ExtensionOf(name string) string {
	i := strings.LastIndexByte(name, '.')
	if i <= 0 || i == len(name)-1 {
		return ""
	}
	return string(FoldString(name[i+1:]))
}

// MaxExts bounds how many extensions one query may name.
//
// High enough that no combination of the interface's own type groups reaches
// it, because a client silently dropping half its filter would answer a
// narrow question with a short list. The cost of the bound is a linear scan
// over an already matched name, so the number is about the client's honesty
// rather than the server's work.
const MaxExts = 128

// ParseExts reads a comma-separated extension list into the form Admits
// compares against: folded, dot-stripped, deduplicated, order preserved.
//
// Blank entries are dropped rather than refused, so "pdf,,txt" and a trailing
// comma both work; a list naming more than MaxExts is refused, because a
// client controls both its length and the per-entry cost of using it.
func ParseExts(raw string) ([]string, bool) {
	if strings.TrimSpace(raw) == "" {
		return nil, true
	}

	var out []string
	for _, part := range strings.Split(raw, ",") {
		ext := string(FoldString(strings.TrimPrefix(strings.TrimSpace(part), ".")))
		if ext == "" {
			continue
		}
		if strings.ContainsAny(ext, "/\x00") {
			return nil, false
		}
		if len(out) >= MaxExts {
			return nil, false
		}
		if !contains(out, ext) {
			out = append(out, ext)
		}
	}
	return out, true
}

func contains(list []string, want string) bool {
	for _, s := range list {
		if s == want {
			return true
		}
	}
	return false
}

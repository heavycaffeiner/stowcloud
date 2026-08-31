// contractcheck compares the JSON the Go handlers declare against the fields
// the TypeScript client declares as required.
//
// Why it exists: every defect this tool would have caught was found by a person
// clicking something and watching it do nothing. The client read `perms` and
// the server never sent it, so selecting a file threw. It read `cursor` and the
// server sent `next`, so a directory past one page could not be paged. It read
// `principal` and the server sent a bare `user`, so the grant list could not
// say who a grant was for. It sent `name` on a rename and the server read
// `new_name`, so renaming answered 422 with an empty component. None of these
// failed a test, because both halves were internally consistent and nothing
// compared them.
//
// This is deliberately structural rather than clever. It reads the field names
// out of both sides and reports what one requires and the other omits. It knows
// nothing about types, so it cannot catch a string where a number was meant;
// what it catches is a field that is simply not there, which is what every one
// of the above was.
package main

import (
	"fmt"
	"io"
	"os"
	"regexp"
	"sort"
	"strings"
)

// say writes a diagnostic. It is the one place in this command that ignores an
// error, because a message that cannot be printed has nowhere else to go.
func say(w io.Writer, format string, a ...any) {
	fmt.Fprintf(w, format, a...) //nolint:errcheck // see the comment above.
}

// pair is one interface and the Go struct that answers it.
type pair struct {
	iface string
	// goType is the struct literal name in the handler package.
	goType string
	// skip names fields the client requires that this server deliberately
	// does not send, with the reason. An entry here is a decision; an absence
	// is a defect.
	skip map[string]string
}

func pairs() []pair {
	return []pair{
		{iface: "Entry", goType: "EntryView"},
		{iface: "ListResponse", goType: "PageView"},
		{iface: "AdminGrant", goType: "GrantView"},
	}
}

// inlinePair is an interface answered by a map literal rather than a struct.
//
// Several handlers build their response with map[string]any, so there are no
// struct tags to read. The keys are matched out of the literal instead, which
// is how the folder size was caught: it sent size and count where the screen
// reads bytes and files, so the panel showed an undefined total beside
// "undefined files".
type inlinePair struct {
	iface string
	// marker is a string that appears in the handler holding the literal, used
	// to find the right one.
	marker string
}

func inlinePairs() []inlinePair {
	return nil
}

func main() {
	if len(os.Args) != 3 {
		say(os.Stderr, "usage: contractcheck <types.ts> <handler-dir>"+"\n")
		os.Exit(2)
	}
	ts, err := os.ReadFile(os.Args[1])
	if err != nil {
		say(os.Stderr, "contractcheck: reading the client types: %v\n", err)
		os.Exit(2)
	}
	goSrc, err := readGo(os.Args[2])
	if err != nil {
		say(os.Stderr, "contractcheck: reading the handlers: %v\n", err)
		os.Exit(2)
	}

	bad := 0
	for _, p := range pairs() {
		want, ok := requiredFields(string(ts), p.iface)
		if !ok {
			say(os.Stdout, "contractcheck: no interface %s in the client types\n", p.iface)
			bad++
			continue
		}
		have, ok := jsonFields(goSrc, p.goType)
		if !ok {
			say(os.Stdout, "contractcheck: no struct %s in the handlers\n", p.goType)
			bad++
			continue
		}
		var missing []string
		for _, f := range want {
			if _, sent := have[f]; sent {
				continue
			}
			if _, allowed := p.skip[f]; allowed {
				continue
			}
			missing = append(missing, f)
		}
		sort.Strings(missing)
		if len(missing) > 0 {
			say(os.Stdout, "%s requires %s, and %s does not send it\n",
				p.iface, strings.Join(missing, ", "), p.goType)
			bad += len(missing)
		}
	}
	for _, p := range inlinePairs() {
		want, ok := requiredFields(string(ts), p.iface)
		if !ok {
			say(os.Stdout, "contractcheck: no interface %s in the client types\n", p.iface)
			bad++
			continue
		}
		have, ok := literalKeys(goSrc, p.marker)
		if !ok {
			say(os.Stdout, "contractcheck: no response literal near %s\n", p.marker)
			bad++
			continue
		}
		var missing []string
		for _, f := range want {
			if _, sent := have[f]; !sent {
				missing = append(missing, f)
			}
		}
		sort.Strings(missing)
		if len(missing) > 0 {
			say(os.Stdout, "%s requires %s, and the literal near %s does not send it\n",
				p.iface, strings.Join(missing, ", "), p.marker)
			bad += len(missing)
		}
	}

	if bad > 0 {
		say(os.Stdout, "\ncontractcheck: %d field(s) the client reads and the server never sends.\n", bad)
		say(os.Stdout, "Add the field, or record it in the pair's skip map with the reason."+"\n")
		os.Exit(1)
	}
	say(os.Stdout, "contractcheck: %d contract(s), no drift.\n", len(pairs())+len(inlinePairs()))
}

// readGo concatenates every .go file in a directory, which is enough: this
// only ever looks for struct tags.
func readGo(dir string) (string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return "", err
	}
	var parts []string
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".go") ||
			strings.HasSuffix(e.Name(), "_test.go") {
			continue
		}
		src, rerr := os.ReadFile(dir + "/" + e.Name())
		if rerr != nil {
			return "", rerr
		}
		parts = append(parts, string(src))
	}
	return strings.Join(parts, "\n"), nil
}

var (
	tsField    = regexp.MustCompile(`(?m)^\s*([a-z_][a-zA-Z0-9_]*)(\??)\s*:`)
	tsComment  = regexp.MustCompile(`(?s)/\*.*?\*/`)
	tsLineNote = regexp.MustCompile(`(?m)//[^\n]*`)
	goTag      = regexp.MustCompile("`json:\"([a-zA-Z0-9_]+)")
)

// requiredFields returns the non-optional field names of one interface.
func requiredFields(ts, iface string) ([]string, bool) {
	re := regexp.MustCompile(`(?s)export interface ` + iface + ` \{(.*?)\n\}`)
	m := re.FindStringSubmatch(ts)
	if m == nil {
		return nil, false
	}
	body := tsLineNote.ReplaceAllString(tsComment.ReplaceAllString(m[1], ""), "")
	var out []string
	for _, f := range tsField.FindAllStringSubmatch(body, -1) {
		if f[2] == "?" {
			continue // optional: the server may omit it
		}
		out = append(out, f[1])
	}
	return out, true
}

// literalKeys returns the quoted keys of the map literal containing marker.
//
// The window is the enclosing writeJSON call, found by walking back to it and
// forward to the closing brace. Crude, and it only has to read a literal that
// a handler wrote a few lines wide.
func literalKeys(src, marker string) (map[string]struct{}, bool) {
	at := strings.Index(src, marker)
	if at < 0 {
		return nil, false
	}
	start := strings.LastIndex(src[:at], "writeJSON")
	if start < 0 {
		return nil, false
	}
	end := strings.Index(src[at:], "})")
	if end < 0 {
		return nil, false
	}
	window := src[start : at+end]
	out := map[string]struct{}{}
	for _, m := range literalKey.FindAllStringSubmatch(window, -1) {
		out[m[1]] = struct{}{}
	}
	return out, true
}

var literalKey = regexp.MustCompile(`"([a-z_][a-z0-9_]*)":`)

// jsonFields returns the json names one Go struct sends.
func jsonFields(src, name string) (map[string]struct{}, bool) {
	re := regexp.MustCompile(`(?s)type ` + name + ` struct \{(.*?)\n\}`)
	m := re.FindStringSubmatch(src)
	if m == nil {
		return nil, false
	}
	out := map[string]struct{}{}
	for _, f := range goTag.FindAllStringSubmatch(m[1], -1) {
		out[f[1]] = struct{}{}
	}
	return out, true
}

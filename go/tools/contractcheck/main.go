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
		// The first-run screen reads these. It described `level` and
		// `reason_key`, which no response has ever carried, so every read was
		// undefined and the screen stopped on a panel with nothing in it.
		{iface: "SetupFinding", goType: "FindingView"},
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

// sendPair is one client request body and the Go struct that decodes it.
//
// The other direction from pair: here the client is the sender, and a field it
// puts on the wire that the struct has no tag for is refused outright, because
// the decoder disallows unknown fields. That is a 400 on a button press, with
// nothing on screen naming the field.
type sendPair struct {
	iface  string
	goType string
	// skip names fields the client sends that this struct deliberately does
	// not decode, with the reason.
	skip map[string]string
}

// sendPairs is every request body whose field names reach the wire unchanged.
//
// The test for membership is the request site, not the name: the type has to
// be JSON.stringify'd directly, or spread into a literal that renames nothing.
// A type the client translates before sending would be compared against a
// shape the server never receives, which is a contract nobody implements.
func sendPairs() []sendPair {
	return []sendPair{
		// The share screen sent host_path and the handler decoded host, so no
		// folder could be added: the request was refused before it reached a
		// line that could explain why.
		{iface: "CreateShareReq", goType: "createShareRequest"},
		{iface: "UpdateShareReq", goType: "updateShareRequest"},

		{iface: "CreateGroupReq", goType: "groupRequest"},
		{iface: "UpdateGroupReq", goType: "groupRequest"},
		{iface: "UpdateGrantReq", goType: "updateGrantRequest"},

		// The link bodies are spread into a literal that rewrites one value
		// and no names: perms goes out as the permission names the server
		// reads rather than the bitmask the screen holds. Every key is the
		// type's own, so comparing names is exactly right.
		{iface: "ShareLinkCreateReq", goType: "createLinkRequest"},
		{iface: "ShareLinkPatchReq", goType: "updateLinkRequest"},

		// Absent, because the client rewrites the field names before sending:
		//
		//   CreateGrantReq — adminCreateGrant turns a `{kind, id}` principal
		//   into the wire's `user` or `group` string and stringifies share.
		//
		//   MoveReq — copy() and move() send one `{from, to, on_conflict}`
		//   per path rather than the batch the type describes.
		//
		// A pair for either would compare the shape before the translation.
	}
}

func main() {
	if len(os.Args) != 4 {
		say(os.Stderr, "usage: contractcheck <types.ts> <handler-dir> <request-dir>"+"\n")
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
	// The request structs live with the handlers that decode them rather than
	// with the views, so they are read from their own directory.
	reqSrc, err := readGo(os.Args[3])
	if err != nil {
		say(os.Stderr, "contractcheck: reading the request decoders: %v\n", err)
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
	// The other direction: a field the client sends that the decoder has no
	// tag for is refused, because it disallows unknown fields. Optional
	// fields count too, since sending one is what trips the refusal.
	for _, p := range sendPairs() {
		send, ok := allFields(string(ts), p.iface)
		if !ok {
			say(os.Stdout, "contractcheck: no interface %s in the client types\n", p.iface)
			bad++
			continue
		}
		decoded, ok := jsonFields(reqSrc, p.goType)
		if !ok {
			say(os.Stdout, "contractcheck: no struct %s in the request decoders\n", p.goType)
			bad++
			continue
		}
		var unknown []string
		for _, f := range send {
			if _, taken := decoded[f]; taken {
				continue
			}
			if _, allowed := p.skip[f]; allowed {
				continue
			}
			unknown = append(unknown, f)
		}
		sort.Strings(unknown)
		if len(unknown) > 0 {
			say(os.Stdout, "%s sends %s, and %s does not decode it: the request is refused\n",
				p.iface, strings.Join(unknown, ", "), p.goType)
			bad += len(unknown)
		}
	}

	if bad > 0 {
		say(os.Stdout, "\ncontractcheck: %d field(s) the two sides disagree on.\n", bad)
		say(os.Stdout, "Match the name, or record it in the pair's skip map with the reason."+"\n")
		os.Exit(1)
	}
	say(os.Stdout, "contractcheck: %d contract(s), no drift.\n",
		len(pairs())+len(inlinePairs())+len(sendPairs()))
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

// allFields returns every field name of one interface, optional included.
//
// Optional matters in the sending direction: a field the client omits is fine,
// and one it sends that the decoder does not know refuses the whole request.
func allFields(ts, iface string) ([]string, bool) {
	re := regexp.MustCompile(`(?s)export interface ` + iface + ` \{(.*?)\n\}`)
	m := re.FindStringSubmatch(ts)
	if m == nil {
		return nil, false
	}
	body := tsLineNote.ReplaceAllString(tsComment.ReplaceAllString(m[1], ""), "")
	var out []string
	for _, f := range tsField.FindAllStringSubmatch(body, -1) {
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

// speccheck reports a documented identifier that no longer exists in the tree
// the document is about.
//
// Why it exists: the rebuild's documents state numbered deliberate changes, and
// most of them name the thing they are about in backticks. Checking that sweep
// by hand takes an afternoon, and its result rots the moment somebody renames a
// symbol or moves a file. What rots quietly is a document describing a system
// that is no longer there, which is the same failure freshscan catches in
// comments, one level up.
//
// It is deliberately narrow. It reads backticked identifiers out of the
// numbered change lists, skips the ones the document itself defers to a later
// phase or states as an absence, and reports what is left and cannot be found.
// It knows nothing about whether the change was implemented correctly: what it
// catches is a name that has gone away, which is what makes the surrounding
// prose wrong.
package main

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// say writes a diagnostic. It is the one place in this command that ignores an
// error, because a message that cannot be printed has nowhere else to go.
func say(w io.Writer, format string, a ...any) {
	fmt.Fprintf(w, format, a...) //nolint:errcheck // see the comment above.
}

// numbered matches a deliberate-change entry: "1. **The thing** ...".
var numbered = regexp.MustCompile(`^[0-9]+\. \*\*`)

// backticked pulls the identifiers a change names.
var backticked = regexp.MustCompile("`([A-Za-z_][A-Za-z0-9_./]*)`")

// deferred marks a change whose own text says it belongs to a later phase.
var deferred = regexp.MustCompile(`(?i)phase [0-9]`)

// absence marks a change asserting that something is gone, or has moved
// somewhere this tool was not pointed at. Its identifier is expected to be
// missing, so looking for it would report every one of them.
//
// Each phrase has to claim removal rather than merely mention the past. "The
// old tree" was here once and skipped a third of the entries, because a change
// routinely explains what was wrong before stating what is true now: the entry
// naming CreateUser reads "The old tree validates at the setup screen only",
// and skipping it meant renaming that method went unreported.
var absence = regexp.MustCompile(`(?i)\bis dropped\b|\bare dropped\b|\bis deleted\b|\bare deleted\b|` +
	`\bdies\b|is severed|drops out|\bis not part of\b|\bno longer exists?\b|` +
	`\bmoves? to\b|\bmoved to\b|\bmoves? out of\b|\bis replaced by\b|\breplaces\b|` +
	`\bdisappears?\b|\bstops [a-z-]+ing\b|\bstop [a-z-]+ing\b`)

// prior matches the words immediately before an identifier that mark it as the
// name the old tree used, rather than the name the rebuild is meant to have.
//
// Applied to a window before each identifier rather than to the whole entry.
// That distinction matters: a whole-entry rule once skipped every change whose
// text mentioned the past, which is most of them, and the check quietly stopped
// working. Matching wrongly here costs one identifier, not the entry.
var prior = regexp.MustCompile(`(?i)(the old|the current|the reference's|extracted from|` +
	`splits out of|moved out of|\bnot\b|instead of|rather than|in place of|` +
	`replacing|,|/)\s*$`)

// intoTail matches an "extracted from A into B" clause up to the "into", so
// every identifier before it is a source name and every one after is a target.
var intoTail = regexp.MustCompile(`(?i)\b(into|becomes?|collapse to|are now|is now)\b`)

// ignored names identifiers that are real but unfindable by this tool: a
// package from another module, a database table, a syscall, a filename the
// tree spells differently. Each entry is a decision; an absence is a defect.
var ignored = map[string]string{
	// Not Go identifiers.
	"config_secret": "a database table, not a symbol",
	"share_grant":   "a database table, not a symbol",
	"share_link":    "a database table, not a symbol",
	"smb.conf":      "the daemon's own configuration file",
	"passdb.tdb":    "the daemon's own account database",

	// Named as the old tree's spelling, which is the point of the sentence.
	"internal/smb":    "the old tree, named because the change is severing it",
	"internal/vfs":    "the old tree, named for the same reason",
	"internal/upload": "the old tree, named for the same reason",

	// Illustrative paths in a worked example, not identifiers.
	"a/b":   "an example path in a grant-subpath illustration",
	"a/b/c": "the same example",

	// An HTTP method, which is a protocol token rather than a symbol.
	"OPTIONS": "an HTTP method named in a route-table rule",

	// A reference to a sibling document, not to code.
	"core/09": "the quota document, cited for the contract this store satisfies",
	"core/10": "the share-link document, cited the same way",
	"core/11": "the homes document, cited the same way",

	// An import the change forbids. Finding it would be the defect.
	"database/sql": "named to say the evaluator must not import it",

	// The old tree's spelling of a file, named to locate the behavior being
	// carried over. Checked by hand: the acl evaluator has no database/sql
	// import, ident carries ToSQL and FromSQL as methods, home.go creates its
	// directory through one helper, and trash.go owns the recursive one.
	"replace_linux_test.go": "the old tree's test file, named as the source of a property",
	"toSQL":                 "the old tree's unexported spelling; it is ident.ToSQL now",
	"fromSQL":               "the same, now ident.FromSQL",
	"ensureDir":             "the old tree's helper, collapsed into the caller",
	"osMkdirAll":            "the same",
	"homes.go":              "the old tree's file, named as the source of the grant INSERT",
}

// changeLines returns the numbered deliberate-change entries in one document,
// joined with their continuation lines so an identifier wrapped onto the next
// line is still seen.
func changeLines(body string) []string {
	var out []string
	var cur []string
	for _, line := range strings.Split(body, "\n") {
		switch {
		case numbered.MatchString(line):
			if len(cur) > 0 {
				out = append(out, strings.Join(cur, " "))
			}
			cur = []string{line}
		case len(cur) > 0 && strings.HasPrefix(line, "   "):
			cur = append(cur, strings.TrimSpace(line))
		case len(cur) > 0:
			out = append(out, strings.Join(cur, " "))
			cur = nil
		}
	}
	if len(cur) > 0 {
		out = append(out, strings.Join(cur, " "))
	}
	return out
}

// finder reports whether an identifier exists somewhere under root.
type finder struct {
	root string
	seen map[string]bool
}

func (f *finder) exists(ident string) bool {
	if got, ok := f.seen[ident]; ok {
		return got
	}
	got := f.lookUp(ident)
	f.seen[ident] = got
	return got
}

func (f *finder) lookUp(ident string) bool {
	// A package-qualified symbol is written kit/num.Narrow: a path with a
	// symbol on the end. The symbol is what to look for, since the path is a
	// directory and the two together are a string that appears nowhere.
	if dot := strings.LastIndex(ident, "."); dot >= 0 && !strings.HasSuffix(ident, ".go") {
		if sym := ident[dot+1:]; sym != "" {
			return f.symbol(sym)
		}
	}
	// A path or filename is looked for as one, since grep would find the
	// string in every import that mentions it.
	if strings.Contains(ident, "/") || strings.HasSuffix(ident, ".go") {
		found := false
		werr := filepath.WalkDir(f.root, func(p string, _ os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if strings.HasSuffix(filepath.ToSlash(p), ident) {
				found = true
			}
			return nil
		})
		if werr != nil {
			// A tree that cannot be walked makes every answer unknown, which
			// is not the same as a name being absent. Reporting it as present
			// is the quiet choice; the walk failing is loud enough on its own.
			say(os.Stderr, "speccheck: walking %s: %v\n", f.root, werr)
			return true
		}
		return found
	}
	return f.symbol(ident)
}

// symbol reports whether a bare identifier appears in the tree, matched on a
// word boundary so Read does not match ReadAll.
func (f *finder) symbol(name string) bool {
	cmd := exec.Command("grep", "-rqlw", "--include=*.go", name, f.root) //nolint:gosec // the root is this tool's argument and the name came from a document in the same repository.
	return cmd.Run() == nil
}

func main() {
	if len(os.Args) != 3 {
		say(os.Stderr, "usage: speccheck <docs-dir> <code-root>\n")
		os.Exit(64)
	}
	docsDir, codeRoot := os.Args[1], os.Args[2]

	f := &finder{root: codeRoot, seen: map[string]bool{}}
	type miss struct{ doc, ident, line string }
	var misses []miss
	items := 0

	err := filepath.WalkDir(docsDir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(path, ".md") {
			return nil
		}
		// The path comes from walking this tool's own documents argument.
		body, rerr := os.ReadFile(path) //nolint:gosec // G304: a path under the caller's own documents directory.
		if rerr != nil {
			return rerr
		}
		for _, line := range changeLines(string(body)) {
			items++
			if deferred.MatchString(line) || absence.MatchString(line) {
				continue
			}
			// Everything before an "into" or a "becomes" in a re-homing
			// sentence names where the code came from. Only the target side
			// has to exist.
			target := line
			if loc := intoTail.FindStringIndex(line); loc != nil {
				target = line[loc[1]:]
			}
			for _, m := range backticked.FindAllStringSubmatchIndex(line, -1) {
				id := line[m[2]:m[3]]
				if ignored[id] != "" || f.exists(id) {
					continue
				}
				if prior.MatchString(line[:m[0]]) {
					continue
				}
				if !strings.Contains(target, "`"+id+"`") {
					continue
				}
				misses = append(misses, miss{filepath.Base(path), id, strings.TrimSpace(line)})
			}
		}
		return nil
	})
	if err != nil {
		say(os.Stderr, "speccheck: %v\n", err)
		os.Exit(2)
	}

	// A scan that found no change entries is broken rather than clean.
	if items == 0 {
		say(os.Stderr, "speccheck: no deliberate-change entries found under %s; the scan is broken\n", docsDir)
		os.Exit(2)
	}

	sort.Slice(misses, func(i, j int) bool { return misses[i].ident < misses[j].ident })
	for _, m := range misses {
		say(os.Stdout, "%s: %q is named by a deliberate change and is nowhere in %s\n", m.doc, m.ident, codeRoot)
		say(os.Stdout, "    %s\n", truncate(m.line, 100))
	}
	if len(misses) > 0 {
		say(os.Stderr, "\nspeccheck: %d documented identifier(s) missing from %s across %d change entries.\n"+
			"The document describes something that is not there. Either the change was not made, or it was made "+
			"under another name and the document needs amending.\n", len(misses), codeRoot, items)
		os.Exit(1)
	}
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

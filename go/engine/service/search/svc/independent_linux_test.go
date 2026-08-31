//go:build linux

package svc

import (
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/heavycaffeiner/stowcloud/go/engine/service/search"
)

// Everything else in this family checks search against itself: the index
// against the walk, both against a corpus this package wrote. Two things agree
// because one was built to match the other, and a shared misunderstanding of
// what "find this name" means would satisfy every one of those tests.
//
// So this checks the answer against tools that did not come from this project
// and do not share its code: GNU find and fd. They walk the same real directory
// and apply their own notion of a substring match on a filename. Where all
// three agree, the answer is not an artefact of this implementation.
//
// The comparison is deliberately narrow. It runs over names, not paths, and
// only for needles long enough for the index to serve, because the two
// divergences the family document records mean a broader comparison would be
// measuring known and deliberate differences rather than testing anything.

// haveTool reports whether an external tool is installed, skipping with the
// reason when it is not.
func haveTool(t *testing.T, name string) string {
	t.Helper()
	path, err := exec.LookPath(name)
	if err != nil {
		t.Skipf("%s is not installed, so there is nothing independent to compare against", name)
	}
	return path
}

// runTool invokes an external tool resolved by LookPath.
//
// The gosec exception sits here rather than at each call site: every caller
// passes a binary LookPath found and arguments that are this test's own
// temporary directory and a needle from the list below.
func runTool(tool string, args ...string) *exec.Cmd {
	cmd := exec.Command(tool, args...) //nolint:gosec // G204: the tool comes from LookPath and the arguments are this test's own temp directory and needles.
	cmd.Stdin = nil
	return cmd
}

// findNames runs GNU find over dir and returns the share-relative paths of
// files whose own name contains needle, case-insensitively.
//
// -iname with surrounding wildcards is find's own substring match. Nothing here
// touches this project's code, which is the point.
func findNames(t *testing.T, tool, dir, needle string) []string {
	t.Helper()
	out, err := runTool(tool, dir, "-type", "f", "-iname", "*"+needle+"*").Output()
	if err != nil {
		t.Fatalf("find: %v", err)
	}
	return relativise(t, dir, strings.Split(strings.TrimSpace(string(out)), "\n"))
}

// fdNames does the same through fd, which is a different implementation again.
func fdNames(t *testing.T, tool, dir, needle string) []string {
	t.Helper()
	// --fixed-strings so a needle is a literal rather than a pattern, and
	// --type file to match find's -type f.
	cmd := runTool(tool, "--fixed-strings", "--ignore-case", "--type", "file",
		"--no-ignore", "--hidden", needle, dir)
	out, err := cmd.Output()
	if err != nil {
		// fd exits non-zero when nothing matched, which is an answer.
		var ee *exec.ExitError
		if !slices.Contains([]string{"exit status 1"}, err.Error()) {
			if ok := asExit(err, &ee); !ok {
				t.Fatalf("fd: %v", err)
			}
		}
	}
	return relativise(t, dir, strings.Split(strings.TrimSpace(string(out)), "\n"))
}

func asExit(err error, target **exec.ExitError) bool {
	ee, ok := err.(*exec.ExitError) //nolint:errorlint // the concrete type is what distinguishes "no match" from a failure to run.
	if ok {
		*target = ee
	}
	return ok
}

// relativise turns absolute paths into the share-relative ones search reports.
func relativise(t *testing.T, dir string, paths []string) []string {
	t.Helper()
	out := make([]string, 0, len(paths))
	for _, p := range paths {
		if p == "" {
			continue
		}
		rel, err := filepath.Rel(dir, p)
		if err != nil {
			t.Fatalf("relativising %q: %v", p, err)
		}
		out = append(out, filepath.ToSlash(rel))
	}
	slices.Sort(out)
	return out
}

// The index's answer, restricted to entries whose own name matches, which is
// the question the external tools are being asked.
func indexedNames(t *testing.T, s *Service, src search.Source, needle string) []string {
	t.Helper()
	res, err := s.Query(t.Context(), []search.Source{src}, QueryOptions{Query: needle, Limit: 1000})
	if err != nil {
		t.Fatalf("querying %q: %v", needle, err)
	}
	if res.Tier != TierIndex {
		t.Fatalf("%q was served by %s, so this compares the walk rather than the index", needle, res.Tier)
	}
	out := make([]string, 0, len(res.Hits))
	for _, h := range res.Hits {
		// The index matches the whole path, which the family document records.
		// Restricting to a name match is what makes the three answers
		// comparable rather than measuring that known difference.
		if strings.Contains(strings.ToLower(filepath.Base(h.Path)), strings.ToLower(needle)) {
			out = append(out, h.Path)
		}
	}
	slices.Sort(out)
	return out
}

// The index agrees with GNU find about which files carry a name.
func TestTheIndexAgreesWithFind(t *testing.T) {
	tool := haveTool(t, "find")
	src, dir := corpus(t, 1, equivalenceCorpus()...)
	s := New(Options{Index: newIndex(t)})
	if _, err := s.Build(t.Context(), []search.Source{src}, func() bool { return true }, nil); err != nil {
		t.Fatal(err)
	}

	for _, needle := range []string{"annual", "beach", "notes", "budget", "abcd", "quarterly", "海辺"} {
		t.Run(needle, func(t *testing.T) {
			want := findNames(t, tool, dir, needle)
			got := indexedNames(t, s, src, needle)
			if !slices.Equal(got, want) {
				t.Errorf("the index and GNU find disagree about %q\n  index: %v\n  find:  %v",
					needle, got, want)
			}
			t.Logf("%-10s find=%d index=%d", needle, len(want), len(got))
		})
	}
}

// And with fd, which is a third implementation.
//
// Two agreeing could still be a coincidence of one shared assumption; three is
// harder to arrange by accident.
func TestTheIndexAgreesWithFd(t *testing.T) {
	tool := haveTool(t, "fd")
	src, dir := corpus(t, 1, equivalenceCorpus()...)
	s := New(Options{Index: newIndex(t)})
	if _, err := s.Build(t.Context(), []search.Source{src}, func() bool { return true }, nil); err != nil {
		t.Fatal(err)
	}

	for _, needle := range []string{"annual", "beach", "notes", "budget", "abcd", "海辺"} {
		t.Run(needle, func(t *testing.T) {
			want := fdNames(t, tool, dir, needle)
			got := indexedNames(t, s, src, needle)
			if !slices.Equal(got, want) {
				t.Errorf("the index and fd disagree about %q\n  index: %v\n  fd:    %v",
					needle, got, want)
			}
		})
	}
}

// What this comparison can and cannot see, measured rather than assumed.
//
// Changing the match predicate (substring to prefix) fails both this comparison
// and the equivalence test. Dropping a trigram fails neither, and that is worth
// stating: the trigram layer selects candidate blocks and matchesFolded decides
// the answer, so a trigram defect costs speed and the exact filter absorbs it.
// A comparison at this level cannot distinguish a slow index from a fast one,
// and does not claim to.
//
// The comparison has to be able to fail, or the agreement above says nothing.
//
// A needle no file carries makes all three answer empty, which two broken
// implementations would also do. This asserts the external tool finds something
// for the needles that matter, so the equality above is over non-empty sets.
func TestTheExternalToolFindsSomethingToCompare(t *testing.T) {
	tool := haveTool(t, "find")
	_, dir := corpus(t, 1, equivalenceCorpus()...)

	for _, c := range []struct {
		needle  string
		atLeast int
	}{
		{"annual", 2},
		{"beach", 2},
		{"notes", 2},
	} {
		if got := findNames(t, tool, dir, c.needle); len(got) < c.atLeast {
			t.Errorf("GNU find reported %d files for %q, want at least %d: %v",
				len(got), c.needle, c.atLeast, got)
		}
	}
}

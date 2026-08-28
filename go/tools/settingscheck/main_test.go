package main

import (
	"os"
	"strings"
	"testing"
)

// The scan reads field names out of the request interfaces and pairs them with
// the section their body is PATCHed to.
func TestTheClientScanReadsFieldsPerSection(t *testing.T) {
	got := clientKeys(`
export interface SearchSettingsReq {
  max_concurrent_fast: number
  walk_deadline_fast_ms: number
}

export interface WatchSettingsReq {
  hot_set_max: number
  force?: boolean
}
`)
	for _, want := range []string{
		"search.max_concurrent_fast",
		"search.walk_deadline_fast_ms",
		"watch.hot_set_max",
		"watch.force",
	} {
		if !got[want] {
			t.Errorf("the scan missed %s: %v", want, got)
		}
	}
	if len(got) != 4 {
		t.Errorf("the scan found %d keys, want 4: %v", len(got), got)
	}
}

// An optional field is written too. The question is whether anything reads the
// key, not whether the client always sends it.
func TestAnOptionalFieldIsStillAWrite(t *testing.T) {
	got := clientKeys("export interface HomesSettingsReq {\n  root?: string | null\n}\n")
	if !got["homes.root"] {
		t.Errorf("an optional field was not counted as a write: %v", got)
	}
}

// A comment inside the interface is prose rather than a field. Counting one
// would invent a key nobody writes and fail the gate for nothing.
func TestCommentsInsideAnInterfaceAreNotFields(t *testing.T) {
	got := clientKeys(`
export interface RateSettingsReq {
  /** requests admitted per second, refilled continuously */
  per_sec: number
  // burst is the bucket depth
  burst: number
}
`)
	if len(got) != 2 {
		t.Errorf("the scan found %d keys, want 2: %v", len(got), got)
	}
}

// An interface this tool does not map to a section is skipped rather than
// guessed at, since the section name is a routing decision.
func TestAnUnmappedInterfaceIsIgnored(t *testing.T) {
	got := clientKeys("export interface SomethingElseReq {\n  field: number\n}\n")
	if len(got) != 0 {
		t.Errorf("an unmapped interface produced %v", got)
	}
}

// The loader scan reads the adjacent section and key literals every reader
// names.
func TestTheLoaderScanReadsBothLiterals(t *testing.T) {
	got := loaderKeys(`
	readInt(all, "search", "max_concurrent_fast", BoundSearchConcurrent(), log, func(v int64) {
		out.SearchConcurrentSSD = int(v)
	})
	readStrings(all, "network", "app_hosts", CheckHost, log, func(v []string) {
		out.AppHosts = v
	})
`)
	if !got["search.max_concurrent_fast"] || !got["network.app_hosts"] {
		t.Errorf("the loader scan found %v", got)
	}
}

// A hyphenated section is a real one: symlink-policy is the route the client
// PATCHes.
func TestAHyphenatedSectionIsRead(t *testing.T) {
	got := loaderKeys(`readString(all, "symlink-policy", "policy", log, set)`)
	if !got["symlink-policy.policy"] {
		t.Errorf("a hyphenated section was not read: %v", got)
	}
}

// The comparison is what the gate reports: a key written and not read, unless
// the allow list says why.
func TestAKeyWrittenAndNotReadIsReported(t *testing.T) {
	writes := map[string]bool{"search.invented": true, "search.max_concurrent_fast": true}
	reads := map[string]bool{"search.max_concurrent_fast": true}

	var missing []string
	for k := range writes {
		if reads[k] || allowed[k] != "" {
			continue
		}
		missing = append(missing, k)
	}
	if len(missing) != 1 || missing[0] != "search.invented" {
		t.Errorf("the comparison reported %v", missing)
	}
}

// Every allow-list entry carries a reason, because an entry with none is an
// exception nobody has to justify.
func TestEveryAllowedKeyCarriesAReason(t *testing.T) {
	for k, why := range allowed {
		if strings.TrimSpace(why) == "" {
			t.Errorf("%s is allowed with no reason", k)
		}
		if !strings.Contains(k, ".") {
			t.Errorf("%q is not a section-qualified key", k)
		}
	}
}

// The allow list is for keys nothing reads. An entry naming a key the loader
// does read is stale, and it would hide a later regression on that key.
func TestNoAllowedKeyIsOneTheLoaderActuallyReads(t *testing.T) {
	// The real loader, so this fails when a key gains a reader and the entry
	// stays behind.
	src, err := readFileForTest("../../internal/runtimecfg/runtimecfg.go")
	if err != nil {
		t.Skipf("the loader is not where this test expects: %v", err)
	}
	reads := loaderKeys(src)
	for k := range allowed {
		if reads[k] {
			t.Errorf("%s is on the allow list and the loader reads it; the entry is stale", k)
		}
	}
}

// Every section the client can PATCH is one this tool knows about. A route
// added without a mapping here silently checks nothing.
func TestEverySectionTheClientPatchesIsMapped(t *testing.T) {
	src, err := readFileForTest("../../../web/src/lib/api/http.ts")
	if err != nil {
		t.Skipf("the client is not where this test expects: %v", err)
	}
	known := map[string]bool{}
	for _, s := range section {
		known[s] = true
	}
	for _, line := range strings.Split(src, "\n") {
		const marker = "'/admin/server-settings/"
		i := strings.Index(line, marker)
		if i < 0 {
			continue
		}
		rest := line[i+len(marker):]
		end := strings.Index(rest, "'")
		if end < 0 {
			continue
		}
		if name := rest[:end]; !known[name] {
			t.Errorf("the client PATCHes %q and this tool has no mapping for it, so its keys are unchecked", name)
		}
	}
}

// readFileForTest reads a file relative to this package, for the two tests that
// check the tool against the real client and loader rather than a fixture.
func readFileForTest(path string) (string, error) {
	b, err := os.ReadFile(path)
	return string(b), err
}

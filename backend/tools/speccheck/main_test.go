package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The scan reads numbered deliberate-change entries and joins their
// continuation lines, because an identifier routinely wraps onto the next one.
func TestTheScanJoinsAWrappedEntry(t *testing.T) {
	got := changeLines("" +
		"## Deliberate changes\n" +
		"\n" +
		"1. **The first thing.** It mentions `FirstSymbol` and continues\n" +
		"   onto a second line naming `SecondSymbol` as well.\n" +
		"2. **The second thing.** Naming `ThirdSymbol`.\n" +
		"\n" +
		"Some prose that is not a change.\n")

	if len(got) != 2 {
		t.Fatalf("the scan found %d entries, want 2: %q", len(got), got)
	}
	if !strings.Contains(got[0], "SecondSymbol") {
		t.Errorf("a wrapped line was dropped: %q", got[0])
	}
	if strings.Contains(got[1], "not a change") {
		t.Errorf("prose after the list was absorbed: %q", got[1])
	}
}

// Prose that is not a numbered entry contributes nothing, or every paragraph
// mentioning a symbol would be treated as a claim about it.
func TestOrdinaryProseIsNotAChangeEntry(t *testing.T) {
	if got := changeLines("Some text naming `Symbol` in passing.\n"); len(got) != 0 {
		t.Errorf("prose produced %d entries: %q", len(got), got)
	}
}

// A change whose own text defers to a later phase is skipped: its subject is
// not built yet, so reporting it is noise on every run.
func TestALaterPhaseEntryIsSkipped(t *testing.T) {
	if !deferred.MatchString("1. **A thing** (Phase 3 amendment): it arrives later.") {
		t.Error("a phase 3 deferral was not recognised")
	}
	if deferred.MatchString("1. **A thing.** It is here now.") {
		t.Error("an ordinary entry was treated as deferred")
	}
}

// An entry claiming something is gone is skipped, since its identifier is
// meant to be absent.
func TestARemovalEntryIsSkipped(t *testing.T) {
	for _, l := range []string{
		"1. **`SetChunkSettings` is dropped**; the other stays.",
		"2. **`PublishNew` is not part of the package.**",
		"3. **The transport moves to the worker package.**",
		"4. **`isUniqueViolation` stops string-matching.**",
		"5. **The five helpers disappear behind the schema.**",
	} {
		if !absence.MatchString(l) {
			t.Errorf("a removal claim was not recognised: %q", l)
		}
	}
}

// The absence rule has to claim removal rather than merely mention the past.
//
// "The old tree" was in this rule once and skipped a third of the entries,
// because a change routinely explains what was wrong before saying what is true
// now. That silently disabled the check for every one of them.
func TestMentioningThePastIsNotARemovalClaim(t *testing.T) {
	for _, l := range []string{
		"1. **Creation-time enforcement.** The old tree validates at the setup screen only; `CreateUser` refuses now.",
		"2. **The limiter gets a mutex.** Behavior under a race was previously undefined.",
	} {
		if absence.MatchString(l) {
			t.Errorf("an entry describing the past was skipped as a removal: %q", l)
		}
	}
}

// A package-qualified symbol is looked up by its symbol, because the whole
// spelling appears nowhere in the source.
func TestAPackageQualifiedSymbolIsFoundByItsSymbol(t *testing.T) {
	dir := t.TempDir()
	write(t, filepath.Join(dir, "x.go"), "package p\n\nfunc Narrow() {}\n")

	f := &finder{root: dir, seen: map[string]bool{}}
	if !f.exists("kit/num.Narrow") {
		t.Error("a package-qualified symbol was not found by its symbol")
	}
	if f.exists("kit/num.Widen") {
		t.Error("a symbol that is not there was reported as present")
	}
}

// A method or field written on its type is looked up the same way.
func TestAMethodOnItsTypeIsFoundByItsName(t *testing.T) {
	dir := t.TempDir()
	write(t, filepath.Join(dir, "x.go"), "package p\n\ntype Core struct{}\n\nfunc (c Core) CreateGrant() {}\n")

	f := &finder{root: dir, seen: map[string]bool{}}
	if !f.exists("Core.CreateGrant") {
		t.Error("a method written on its type was not found")
	}
}

// A filename is matched as a path rather than as a word, or the string would
// be found in every import that mentions the package.
func TestAFilenameIsMatchedAsAPath(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "service", "auth"), 0o755); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(dir, "service", "auth", "username.go"), "package auth\n")

	f := &finder{root: dir, seen: map[string]bool{}}
	if !f.exists("username.go") {
		t.Error("a file that exists was not found")
	}
	if f.exists("nosuchfile.go") {
		t.Error("a file that does not exist was reported as present")
	}
}

// A word-boundary match, so a symbol is not found inside a longer one.
func TestASymbolIsNotFoundInsideALongerOne(t *testing.T) {
	dir := t.TempDir()
	write(t, filepath.Join(dir, "x.go"), "package p\n\nfunc ReadAll() {}\n")

	f := &finder{root: dir, seen: map[string]bool{}}
	if f.exists("Read") {
		t.Error("Read matched inside ReadAll")
	}
}

// Every ignored entry carries a reason, because an entry with none is an
// exception nobody has to justify.
func TestEveryIgnoredIdentifierCarriesAReason(t *testing.T) {
	for k, why := range ignored {
		if strings.TrimSpace(why) == "" {
			t.Errorf("%q is ignored with no reason", k)
		}
	}
}

func write(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

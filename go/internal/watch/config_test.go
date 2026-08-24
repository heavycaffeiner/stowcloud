package watch

import "testing"

// F12. The configuration used to accept "fanotify", warn, and do something
// else, so an operator who set it believed they had whole-mount watching and
// had per-directory watching. An unknown value is a refusal now, not a warning
// and a fallback.
func TestFanotifyIsRefusedRatherThanDowngraded(t *testing.T) {
	for _, in := range []string{"fanotify", "hotset", "portable", "inotify_full", "", "INOTIFY"} {
		if _, err := ParseBackend(in); err == nil {
			t.Fatalf("ParseBackend(%q) was accepted", in)
		}
	}
}

func TestInotifyIsTheOneBackend(t *testing.T) {
	b, err := ParseBackend("inotify")
	if err != nil {
		t.Fatalf("ParseBackend: %v", err)
	}
	if b != BackendInotify || b.String() != "inotify" {
		t.Fatalf("backend = %v (%q)", b, b.String())
	}
}

// A partially configured watcher must not end up spinning at zero intervals.
func TestZeroFieldsFallBackToTheDefaults(t *testing.T) {
	got := Config{}.withDefaults()
	want := DefaultConfig()
	if got != want {
		t.Fatalf("withDefaults = %+v, want %+v", got, want)
	}

	custom := Config{HotSetMax: 7}.withDefaults()
	if custom.HotSetMax != 7 {
		t.Fatalf("a set field was overwritten: %+v", custom)
	}
	if custom.Debounce != want.Debounce {
		t.Fatalf("an unset field was left at zero: %+v", custom)
	}
}

package main

import (
	"strings"
	"testing"
)

func TestReportsEveryLeakInTheFixture(t *testing.T) {
	got, err := check([]string{"./testdata/bad"})
	if err != nil {
		t.Fatal(err)
	}
	// One per function in the fixture: the verb that skips String, the
	// pointer, the struct field, the accessor, and the accessor buried in an
	// expression.
	if len(got) != 5 {
		t.Fatalf("found %d leaks, want 5:\n%s", len(got), strings.Join(got, "\n"))
	}
	for _, want := range []string{"bad.go:19", "bad.go:22", "bad.go:25", "bad.go:28", "bad.go:31"} {
		if !strings.Contains(strings.Join(got, "\n"), want) {
			t.Errorf("no finding at %s:\n%s", want, strings.Join(got, "\n"))
		}
	}
}

func TestStaysQuietOnTheGoodFixture(t *testing.T) {
	got, err := check([]string{"./testdata/good"})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("false positives:\n%s", strings.Join(got, "\n"))
	}
}

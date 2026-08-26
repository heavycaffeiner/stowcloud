//go:build linux

package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// The crash-loop conclusion.
//
// A stored setting that stops the engine coming up produces the same failure
// every start, and a supervisor restarts the process. From the outside that is
// an undifferentiated spin: the same lines repeating, with nothing saying
// whether this is the first attempt or the fortieth.

// testNow is the instant every case here measures from. The window arithmetic
// is what is under test, so a fixed point makes the cases reproducible and
// keeps the wall clock out of a tree that reads it in one package.
func testNow() time.Time { return time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC) }

func TestThreeFailuresInsideTheWindowIsALoop(t *testing.T) {
	dir := t.TempDir()
	now := testNow()

	for i := 1; i <= 2; i++ {
		attempts, looping := recordEngineFailure(dir, now.Add(time.Duration(i)*time.Second))
		if looping {
			t.Fatalf("attempt %d already concluded a loop", i)
		}
		if attempts != i {
			t.Errorf("attempt %d counted as %d", i, attempts)
		}
	}
	attempts, looping := recordEngineFailure(dir, now.Add(3*time.Second))
	if !looping || attempts != engineLoopCount {
		t.Fatalf("the third failure did not conclude a loop: %d, %v", attempts, looping)
	}
}

// Failures older than the window do not count, so a deployment that failed
// once a week for a month is not held on the strength of history.
func TestFailuresOutsideTheWindowAreForgotten(t *testing.T) {
	dir := t.TempDir()
	now := testNow()

	recordEngineFailure(dir, now.Add(-2*engineLoopWindow))
	recordEngineFailure(dir, now.Add(-2*engineLoopWindow))
	attempts, looping := recordEngineFailure(dir, now)
	if looping {
		t.Error("stale failures concluded a loop")
	}
	if attempts != 1 {
		t.Errorf("attempts = %d, want only the recent one", attempts)
	}
}

// A start that got the engine up forgets the history, which is what makes the
// count mean "consecutive failures": a deployment that has run for a month and
// then fails once gets three fresh attempts rather than the conclusion.
func TestAHealthyStartClearsTheHistory(t *testing.T) {
	dir := t.TempDir()
	now := testNow()

	recordEngineFailure(dir, now)
	recordEngineFailure(dir, now)
	clearEngineFailures(dir)

	attempts, looping := recordEngineFailure(dir, now)
	if looping || attempts != 1 {
		t.Fatalf("the history survived a healthy start: %d, %v", attempts, looping)
	}
}

// A file somebody edited, or a half-written one, starts the count again rather
// than suppressing the conclusion forever.
func TestACorruptCounterStartsOver(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, engineFailureFile)
	if err := os.WriteFile(path, []byte("not json at all"), 0o600); err != nil {
		t.Fatalf("writing: %v", err)
	}
	attempts, looping := recordEngineFailure(dir, testNow())
	if looping || attempts != 1 {
		t.Fatalf("a corrupt file produced %d, %v", attempts, looping)
	}
}

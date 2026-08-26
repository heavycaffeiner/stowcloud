// Linux only, like the server it counts failures for.
//go:build linux

package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"time"
)

// Counting engine failures across process starts.
//
// A stored setting that stops the engine coming up produces the same failure
// every time the process starts, and a supervisor restarts the process. From
// the outside that is an undifferentiated spin: the same lines repeating, with
// nothing saying whether this is the first attempt or the fortieth, and
// nothing distinguishing a transient failure that will clear from a stored
// value that never will.
//
// The count is what turns the spin into three attempts and a conclusion. It
// has to live on disk because each attempt is a different process.

// engineFailureFile holds the recent failure timestamps.
const engineFailureFile = ".engine-failures"

// The loop's shape: this many failures inside this window is a stored value
// that is not going to start working, rather than a disk that was slow to
// mount. One minute because a supervisor's restart delay is seconds, so three
// attempts inside one is a machine retrying rather than a person.
const (
	engineLoopCount  = 3
	engineLoopWindow = time.Minute
)

// recordEngineFailure appends this failure and reports how many are recent.
//
// looping is true once the window holds enough of them. A file that cannot be
// read or written answers one attempt and no loop: the count is a diagnostic,
// and a diagnostic that refuses to work must not change what the process does.
func recordEngineFailure(dataDir string, now time.Time) (attempts int, looping bool) {
	path := filepath.Join(dataDir, engineFailureFile)
	recent := append(readFailures(path, now), now.UnixNano())

	// Bounded: only what the window holds is ever written back, so a
	// deployment that has failed for a month does not grow a file.
	if err := os.WriteFile(path, mustJSON(recent), 0o600); err != nil { //nolint:gosec // G703 reads the variable: the name is this file's own constant under the operator's data directory.
		return len(recent), len(recent) >= engineLoopCount
	}
	return len(recent), len(recent) >= engineLoopCount
}

// clearEngineFailures forgets the history, which a start that got the engine
// up is entitled to do.
func clearEngineFailures(dataDir string) {
	// A file that will not go away is not worth failing a healthy start over;
	// the next failure rewrites it with only what its own window holds.
	//nolint:gosec // G703 reads the variable: the name is this file's own constant under the operator's data directory.
	_ = os.Remove(filepath.Join(dataDir, engineFailureFile)) //nolint:errcheck // see above.
}

// readFailures is the timestamps still inside the window.
func readFailures(path string, now time.Time) []int64 {
	raw, err := os.ReadFile(filepath.Clean(path))
	if err != nil {
		return nil
	}
	var all []int64
	if jerr := json.Unmarshal(raw, &all); jerr != nil {
		// A file somebody edited or a half-written one. Starting the count
		// again is the right answer: the alternative is a corrupt file that
		// suppresses the conclusion forever.
		return nil
	}
	cutoff := now.Add(-engineLoopWindow).UnixNano()
	out := make([]int64, 0, len(all))
	for _, ts := range all {
		if ts >= cutoff && ts <= now.UnixNano() {
			out = append(out, ts)
		}
	}
	return out
}

func mustJSON(v []int64) []byte {
	// A list of integers cannot fail to encode, and a nil here would write an
	// empty file that reads back as no history, which is the safe direction
	// anyway.
	out, _ := json.Marshal(v) //nolint:errcheck // see above.
	return out
}

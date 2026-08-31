// Linux only, because it serves a Linux-only engine.
//go:build linux

// The periodic task table.
//
// Data rather than a set of goroutines started at assembly. A table can be
// checked at startup: every task an owning document requires appears exactly
// once, which is what stops a sweep being dropped in a refactor and nobody
// noticing until a database has grown for a month.
package server

import (
	"context"
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"
)

// PeriodicTask is one recurring job.
type PeriodicTask struct {
	// Name is what the startup check and the logs call it.
	Name string
	// Every is how often it runs. Zero is invalid: a task with no interval
	// would either spin or never run, and neither is what a caller meant.
	Every time.Duration
	// Run does the work. It receives a context that is cancelled at shutdown
	// and carries nothing from any request.
	Run func(context.Context) error
}

// RequiredTasks names every task an owning document requires, with the reason
// it exists.
//
// Written here rather than derived from whatever the assembly happened to
// register, because deriving it from the table would make the check tautology:
// the table would always contain exactly what the table contains.
func RequiredTasks() map[string]string {
	return map[string]string{
		"share.probe":        "rechecks share roots so a vanished mount shows as broken rather than as an empty directory",
		"dav.locks.sweep":    "expires WebDAV locks whose holder never came back",
		"login.flow.sweep":   "expires single-sign-on flows that were started and never finished",
		"upload.sweep":       "collects abandoned upload sessions and their part files",
		"auth.maintenance":   "expires sessions and trims the audit log",
		"search.maintenance": "keeps the index in step with the corpus",
		"cache.maintenance":  "trims the rebuildable cache",
		"watch.maintenance":  "releases watches whose subscribers are gone",
	}
}

// ValidateTasks reports every way a table is not the table this server needs.
//
// All the problems at once, since the table is assembled in one place and an
// operator or developer reading them together beats being walked through them
// one restart at a time.
func ValidateTasks(tasks []PeriodicTask) error {
	var problems []string

	seen := map[string]int{}
	for i, t := range tasks {
		switch {
		case strings.TrimSpace(t.Name) == "":
			problems = append(problems, fmt.Sprintf("the task at position %d has no name", i))
			continue
		case t.Every <= 0:
			problems = append(problems,
				fmt.Sprintf("the task %s has no interval", t.Name))
		case t.Run == nil:
			problems = append(problems,
				fmt.Sprintf("the task %s has no function", t.Name))
		}
		seen[t.Name]++
	}

	for name, n := range seen {
		if n > 1 {
			// Twice is worse than not at all for a sweep: two passes over the
			// same rows at the same moment is the shape of a delete racing a
			// read.
			problems = append(problems, fmt.Sprintf("the task %s appears %d times", name, n))
		}
	}

	required := RequiredTasks()
	for name, why := range required {
		if seen[name] == 0 {
			problems = append(problems, fmt.Sprintf("the task %s is missing: it %s", name, why))
		}
	}
	for name := range seen {
		if _, ok := required[name]; !ok {
			// An unrequired task is not refused, but it is named: a table
			// growing entries nobody asked for is how a server acquires work
			// no document explains.
			problems = append(problems, fmt.Sprintf("the task %s is not required by any document", name))
		}
	}

	if len(problems) == 0 {
		return nil
	}
	sort.Strings(problems)
	return fmt.Errorf("periodic tasks: %s", strings.Join(problems, "; "))
}

// TaskNames lists a table's names, sorted, for a diagnostic.
func TaskNames(tasks []PeriodicTask) []string {
	out := make([]string, 0, len(tasks))
	for _, t := range tasks {
		out = append(out, t.Name)
	}
	sort.Strings(out)
	return slices.Compact(out)
}

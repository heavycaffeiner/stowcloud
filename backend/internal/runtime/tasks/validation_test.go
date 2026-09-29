// Linux only, matching the file under test.
//go:build linux

package tasks

import (
	"context"
	"strings"
	"testing"
	"time"
)

// completeTable is a table with every required task, which is what the
// assembly is meant to produce.
func completeTable() []Task {
	tasks := make([]Task, 0, len(RequiredTasks()))
	for name := range RequiredTasks() {
		tasks = append(tasks, Task{
			Name:  name,
			Every: 30 * time.Second,
			Run:   func(context.Context) error { return nil },
		})
	}
	return tasks
}

// A complete table is accepted.
func TestACompleteTaskTableIsAccepted(t *testing.T) {
	if err := Validate(completeTable()); err != nil {
		t.Fatalf("a complete table: %v", err)
	}
	if len(RequiredTasks()) != 6 {
		t.Errorf("got %d required tasks, want 6 real tasks", len(RequiredTasks()))
	}
}

// A missing task is named, and the message says what it does. A dropped sweep
// is invisible until a database has grown for a month, so the report has to be
// enough to act on.
func TestAMissingTaskIsNamedWithItsReason(t *testing.T) {
	table := completeTable()
	dropped := table[0].Name
	table = table[1:]

	err := Validate(table)
	if err == nil {
		t.Fatal("a table missing a required task was accepted")
	}
	if !strings.Contains(err.Error(), dropped) {
		t.Errorf("the report does not name %s: %v", dropped, err)
	}
	// The reason travels, so whoever reads it knows what stopped happening.
	if !strings.Contains(err.Error(), RequiredTasks()[dropped]) {
		t.Errorf("the report omits the reason: %v", err)
	}
}

// A task registered twice is refused. Two passes over the same rows at the
// same moment is the shape of a delete racing a read.
func TestADuplicateTaskIsRefused(t *testing.T) {
	table := completeTable()
	table = append(table, table[0])

	if err := Validate(table); err == nil {
		t.Fatal("a duplicated task was accepted")
	}
}

// A task with no interval, no function or no name is refused rather than
// registered as something that will never run, will spin, or cannot be told
// apart from another.
func TestAnIncompleteTaskIsRefused(t *testing.T) {
	run := func(context.Context) error { return nil }
	for _, c := range []struct {
		what string
		task Task
	}{
		{"no interval", Task{Name: "share.probe", Run: run}},
		{"a negative interval", Task{Name: "share.probe", Every: -time.Second, Run: run}},
		{"no function", Task{Name: "share.probe", Every: time.Second}},
	} {
		// Replace the required task of the same name, so the only problem is
		// the one under test.
		table := completeTable()
		for i := range table {
			if table[i].Name == c.task.Name {
				table[i] = c.task
			}
		}
		if err := Validate(table); err == nil {
			t.Errorf("%s was accepted", c.what)
		}
	}

	// A nameless task is added rather than substituted, or the report would
	// also carry the missing task it replaced.
	if err := Validate(append(completeTable(), Task{Every: time.Second, Run: run})); err == nil {
		t.Error("a task with no name was accepted")
	}
}

// A task no document requires is named. A table growing entries nobody asked
// for is how a server acquires work no document explains.
func TestAnUnrequiredTaskIsNamed(t *testing.T) {
	table := append(completeTable(), Task{
		Name:  "something.nobody.asked.for",
		Every: time.Second,
		Run:   func(context.Context) error { return nil },
	})

	err := Validate(table)
	if err == nil {
		t.Fatal("an unrequired task was accepted silently")
	}
	if !strings.Contains(err.Error(), "something.nobody.asked.for") {
		t.Errorf("the report does not name it: %v", err)
	}
}

// Every problem is reported at once, since the table is assembled in one place
// and reading them together beats one restart at a time.
func TestEveryTaskProblemIsReportedAtOnce(t *testing.T) {
	table := []Task{
		{Name: "share.probe", Every: time.Second, Run: func(context.Context) error { return nil }},
		{Name: "share.probe", Every: time.Second, Run: func(context.Context) error { return nil }},
		{Name: "stray", Every: time.Second, Run: func(context.Context) error { return nil }},
	}

	err := Validate(table)
	if err == nil {
		t.Fatal("a table with several problems was accepted")
	}
	// The duplicate, every missing task and the stray one are all named, not
	// just the first problem found.
	msg := err.Error()
	for name := range RequiredTasks() {
		if !strings.Contains(msg, name) {
			t.Errorf("the report omits %s:\n  %s", name, msg)
		}
	}
	if !strings.Contains(msg, "stray") {
		t.Errorf("the report omits the stray task:\n  %s", msg)
	}
}

// The required list is written out rather than derived from a table, or the
// check would be a tautology: the table would always hold what the table
// holds.
func TestTheRequiredListIsIndependent(t *testing.T) {
	// An empty table fails against every required name, which it could not do
	// if the list came from the table.
	err := Validate(nil)
	if err == nil {
		t.Fatal("an empty table was accepted")
	}
	for name := range RequiredTasks() {
		if !strings.Contains(err.Error(), name) {
			t.Errorf("the report omits %s", name)
		}
	}
}

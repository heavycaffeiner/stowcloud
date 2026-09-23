// Linux only, matching the file under test.
//go:build linux

package server

import (
	"context"
	"strings"
	"testing"
	"time"
)

// completeTable is a table with every required task, which is what the
// assembly is meant to produce.
func completeTable() []PeriodicTask {
	tasks := make([]PeriodicTask, 0, len(RequiredTasks()))
	for name := range RequiredTasks() {
		tasks = append(tasks, PeriodicTask{
			Name:  name,
			Every: 30 * time.Second,
			Run:   func(context.Context) error { return nil },
		})
	}
	return tasks
}

// A complete table is accepted.
func TestACompleteTaskTableIsAccepted(t *testing.T) {
	if err := ValidateTasks(completeTable()); err != nil {
		t.Fatalf("a complete table: %v", err)
	}
	if len(RequiredTasks()) < 8 {
		t.Errorf("only %d tasks are required", len(RequiredTasks()))
	}
}

// A missing task is named, and the message says what it does. A dropped sweep
// is invisible until a database has grown for a month, so the report has to be
// enough to act on.
func TestAMissingTaskIsNamedWithItsReason(t *testing.T) {
	table := completeTable()
	dropped := table[0].Name
	table = table[1:]

	err := ValidateTasks(table)
	if err == nil {
		t.Fatal("a table missing a required task was accepted")
	}
	if !strings.Contains(err.Error(), dropped) {
		t.Errorf("the report does not name %s: %v", dropped, err)
	}
	if !strings.Contains(err.Error(), "is missing") {
		t.Errorf("the report does not say it is missing: %v", err)
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

	err := ValidateTasks(table)
	if err == nil {
		t.Fatal("a duplicated task was accepted")
	}
	if !strings.Contains(err.Error(), "appears 2 times") {
		t.Errorf("the report says %q", err)
	}
}

// A task with no interval or no function is refused rather than registered as
// something that will never run or will spin.
func TestAnIncompleteTaskIsRefused(t *testing.T) {
	for _, c := range []struct {
		what string
		task PeriodicTask
		want string
	}{
		{
			"no interval",
			PeriodicTask{Name: "share.probe", Run: func(context.Context) error { return nil }},
			"no interval",
		},
		{
			"a negative interval",
			PeriodicTask{Name: "share.probe", Every: -time.Second, Run: func(context.Context) error { return nil }},
			"no interval",
		},
		{
			"no function",
			PeriodicTask{Name: "share.probe", Every: time.Second},
			"no function",
		},
		{
			"no name",
			PeriodicTask{Every: time.Second, Run: func(context.Context) error { return nil }},
			"has no name",
		},
	} {
		// Replace the first required task with the broken one, so the only
		// problem is the one under test.
		table := completeTable()
		for i := range table {
			if table[i].Name == "share.probe" {
				table[i] = c.task
				break
			}
		}
		err := ValidateTasks(table)
		if err == nil {
			t.Errorf("%s was accepted", c.what)
			continue
		}
		if !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s reported %q, which does not mention %q", c.what, err, c.want)
		}
	}
}

// A task no document requires is named. A table growing entries nobody asked
// for is how a server acquires work no document explains.
func TestAnUnrequiredTaskIsNamed(t *testing.T) {
	table := append(completeTable(), PeriodicTask{
		Name:  "something.nobody.asked.for",
		Every: time.Second,
		Run:   func(context.Context) error { return nil },
	})

	err := ValidateTasks(table)
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
	table := []PeriodicTask{
		{Name: "share.probe", Every: time.Second, Run: func(context.Context) error { return nil }},
		{Name: "share.probe", Every: time.Second, Run: func(context.Context) error { return nil }},
		{Name: "stray", Every: time.Second, Run: func(context.Context) error { return nil }},
	}

	err := ValidateTasks(table)
	if err == nil {
		t.Fatal("a table with several problems was accepted")
	}
	msg := err.Error()
	for _, want := range []string{"appears 2 times", "is missing", "not required"} {
		if !strings.Contains(msg, want) {
			t.Errorf("the report omits %q:\n  %s", want, msg)
		}
	}
	// Every missing task is named, not just the first.
	missing := 0
	for name := range RequiredTasks() {
		if strings.Contains(msg, name+" is missing") {
			missing++
		}
	}
	if missing != len(RequiredTasks())-1 {
		t.Errorf("%d missing tasks were named, want %d", missing, len(RequiredTasks())-1)
	}
}

// The required list is written out rather than derived from a table, or the
// check would be a tautology: the table would always hold what the table
// holds.
func TestTheRequiredListIsIndependent(t *testing.T) {
	// An empty table fails against every required name, which it could not do
	// if the list came from the table.
	err := ValidateTasks(nil)
	if err == nil {
		t.Fatal("an empty table was accepted")
	}
	for name := range RequiredTasks() {
		if !strings.Contains(err.Error(), name) {
			t.Errorf("the report omits %s", name)
		}
	}
}

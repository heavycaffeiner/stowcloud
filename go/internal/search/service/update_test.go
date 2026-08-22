//go:build linux

package service

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/heavycaffeiner/stowcloud/go/internal/search"
	"github.com/heavycaffeiner/stowcloud/go/internal/watch"
)

// Keeping the index current.
//
// The check is always a query, never an entry count. An update that wrote the
// right records somewhere no query reads is the exact failure this whole file
// exists for: the index was filled once and frozen, every test passed, and a
// file created after the build was missing from every result with nothing
// saying the result was short.

// updateFixture is buildFixture plus where the share lives on disk, so a test
// can create a file behind the index's back the way a person with a shell
// would.
func updateFixture(t *testing.T, names []string) (*Service, []search.Source, string) {
	t.Helper()
	dir := t.TempDir()
	svc, sources := buildFixtureIn(t, dir, names)
	return svc, sources, filepath.Join(dir, "share")
}

func updaterFor(t *testing.T, svc *Service, sources []search.Source) *Updater {
	t.Helper()
	return NewUpdater(svc, func() []search.Source { return sources }, nil)
}

// hits is what a query finds, which is the only thing that matters about an
// update.
func hits(t *testing.T, svc *Service, sources []search.Source, q string) []string {
	t.Helper()
	res, err := svc.Query(context.Background(), sources, QueryOptions{Query: q, Limit: 50})
	if err != nil {
		t.Fatalf("querying %q: %v", q, err)
	}
	out := make([]string, 0, len(res.Hits))
	for _, h := range res.Hits {
		out = append(out, h.Name)
	}
	return out
}

// The defect this fixes: a file created after the build is invisible to a
// query the index answers, and the answer carries a success status.
func TestAFileCreatedAfterTheBuildIsFoundOnceItsDirectoryIsReconciled(t *testing.T) {
	svc, sources, host := updateFixture(t, []string{"docs/report.txt"})
	if _, err := svc.Build(context.Background(), sources, nil, nil); err != nil {
		t.Fatalf("building: %v", err)
	}

	// Created behind the index's back, which is what every write outside this
	// server looks like and what most writes inside it look like too.
	if err := os.WriteFile(filepath.Join(host, "docs", "invoice.txt"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}

	if got := hits(t, svc, sources, "invoice"); len(got) != 0 {
		t.Fatalf("the index found %v before anything reconciled it", got)
	}

	u := updaterFor(t, svc, sources)
	u.apply(context.Background(), watch.InvalEvent{Share: 1, Dir: "docs"})

	got := hits(t, svc, sources, "invoice")
	if len(got) != 1 || got[0] != "invoice.txt" {
		t.Fatalf("after reconciling, the query found %v, want [invoice.txt]", got)
	}
}

// A deleted file has to leave the index. The stat revalidation already stops
// it being returned, so what this protects is the index itself: an entry
// nothing removes is one the next merge writes into the base segment and every
// query keeps scanning.
func TestADeletedFileLeavesTheIndex(t *testing.T) {
	svc, sources, host := updateFixture(t, []string{"docs/report.txt", "docs/invoice.txt"})
	if _, err := svc.Build(context.Background(), sources, nil, nil); err != nil {
		t.Fatalf("building: %v", err)
	}

	if err := os.Remove(filepath.Join(host, "docs", "invoice.txt")); err != nil {
		t.Fatal(err)
	}

	u := updaterFor(t, svc, sources)
	u.apply(context.Background(), watch.InvalEvent{Share: 1, Dir: "docs"})

	ix := svc.index()
	held, err := ix.ChildrenOf(1, "docs")
	if err != nil {
		t.Fatalf("ChildrenOf: %v", err)
	}
	for _, p := range held {
		if p == "docs/invoice.txt" {
			t.Fatal("the deleted file is still in the index")
		}
	}
	if len(held) != 1 {
		t.Fatalf("the index holds %v, want only the remaining file", held)
	}
}

// Reconciling an unchanged directory has to write nothing. An update that
// re-appends the listing grows the overlay on every touch of one file, until
// the merge that collapses it is the only thing the index does.
func TestReconcilingAnUnchangedDirectoryWritesNothing(t *testing.T) {
	svc, sources := buildFixture(t, []string{"docs/report.txt", "docs/invoice.txt"})
	if _, err := svc.Build(context.Background(), sources, nil, nil); err != nil {
		t.Fatalf("building: %v", err)
	}

	ix := svc.index()
	before := ix.Stats()

	u := updaterFor(t, svc, sources)
	for range 5 {
		u.apply(context.Background(), watch.InvalEvent{Share: 1, Dir: "docs"})
	}

	after := ix.Stats()
	if after.DeltaBytes != before.DeltaBytes || after.TombBytes != before.TombBytes {
		t.Fatalf("five reconciles of an unchanged directory grew the overlay from %d/%d to %d/%d bytes",
			before.DeltaBytes, before.TombBytes, after.DeltaBytes, after.TombBytes)
	}
}

// A lost-events notification names no directory. There is nothing to re-read
// and nothing to compare, so the index is left as it is: dropping it would
// turn every query into a walk until somebody noticed and rebuilt.
func TestALostEventsNotificationLeavesTheIndexAlone(t *testing.T) {
	svc, sources := buildFixture(t, []string{"docs/report.txt"})
	if _, err := svc.Build(context.Background(), sources, nil, nil); err != nil {
		t.Fatalf("building: %v", err)
	}

	u := updaterFor(t, svc, sources)
	u.apply(context.Background(), watch.InvalEvent{Share: 1, All: true})

	if got := hits(t, svc, sources, "report"); len(got) != 1 {
		t.Fatalf("the index answered %v after a lost-events event, want the file it held", got)
	}
}

// An event for a share this build does not serve is ignored rather than
// reconciled against nothing, which would tombstone another share's entries.
func TestAnEventForAnUnknownShareIsIgnored(t *testing.T) {
	svc, sources := buildFixture(t, []string{"docs/report.txt"})
	if _, err := svc.Build(context.Background(), sources, nil, nil); err != nil {
		t.Fatalf("building: %v", err)
	}

	u := updaterFor(t, svc, sources)
	u.apply(context.Background(), watch.InvalEvent{Share: 99, Dir: "docs"})

	if got := hits(t, svc, sources, "report"); len(got) != 1 {
		t.Fatalf("an event for another share changed this one: %v", got)
	}
}

// The queue drops rather than blocks. Its producer is the watcher's fan-out,
// which also feeds every connected client's change channel: an index update
// must never be what makes a listing go stale in a browser.
func TestAFullQueueDropsRatherThanBlocking(t *testing.T) {
	svc, sources := buildFixture(t, []string{"docs/report.txt"})
	u := updaterFor(t, svc, sources)

	// Nothing is consuming, so this fills the queue and then keeps going.
	// A blocking implementation never returns from this loop.
	for range updateQueue + 100 {
		u.Offer(watch.InvalEvent{Share: 1, Dir: "docs"})
	}
}

// A directory that vanished takes its entries with it. Reading it fails, and
// the empty listing that produces is what tombstones them.
func TestADeletedDirectoryTakesItsEntriesOut(t *testing.T) {
	svc, sources, host := updateFixture(t, []string{"docs/report.txt", "keep/other.txt"})
	if _, err := svc.Build(context.Background(), sources, nil, nil); err != nil {
		t.Fatalf("building: %v", err)
	}

	if err := os.RemoveAll(filepath.Join(host, "docs")); err != nil {
		t.Fatal(err)
	}

	u := updaterFor(t, svc, sources)
	u.apply(context.Background(), watch.InvalEvent{Share: 1, Dir: "docs"})

	held, err := svc.index().ChildrenOf(1, "docs")
	if err != nil {
		t.Fatalf("ChildrenOf: %v", err)
	}
	if len(held) != 0 {
		t.Fatalf("the index still holds %v for a directory that is gone", held)
	}

	// And the untouched directory is untouched: a reconcile of one directory
	// must not reach another.
	if got := hits(t, svc, sources, "other"); len(got) != 1 {
		t.Fatalf("the sibling directory lost its entries: %v", got)
	}
}

// The updater reads the live index per event rather than capturing one, so
// the administrator's switch turning the index on later in the same process
// does not leave the updater writing into an index nothing queries.
func TestTheUpdaterFollowsTheIndexBeingSwitchedOff(t *testing.T) {
	svc, sources := buildFixture(t, []string{"docs/report.txt"})
	if _, err := svc.Build(context.Background(), sources, nil, nil); err != nil {
		t.Fatalf("building: %v", err)
	}

	svc.SetIndex(nil)
	u := updaterFor(t, svc, sources)
	// No index to update. This must be a no-op rather than a panic.
	u.apply(context.Background(), watch.InvalEvent{Share: 1, Dir: "docs"})
}

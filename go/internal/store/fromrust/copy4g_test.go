package fromrust_test

import (
	"context"
	"testing"

	"github.com/heavycaffeiner/stowcloud/go/internal/store/fromrust"
)

// The Phase 4 import contract: imported share definitions and overrides keep
// their externally visible ids, and an imported running job reads back as
// interrupted with its prior progress and results preserved.

func TestImportPreservesShareIdsAndOverrides(t *testing.T) {
	dir := rustDir(t)
	if _, err := fromrust.Import(context.Background(), dir, testClock()); err != nil {
		t.Fatalf("Import: %v", err)
	}
	d := openState(t, dir)
	ctx := context.Background()

	// The admin-created share keeps its external id: the dynamic-share base is
	// added at registration, never here, so the row holds the id the grants
	// and API payloads already refer to.
	var (
		id   int64
		name string
		host string
	)
	if err := d.SQL().QueryRowContext(ctx,
		`SELECT id, name, host_path FROM share_definition`).Scan(&id, &name, &host); err != nil {
		t.Fatalf("reading the imported share: %v", err)
	}
	if id != 7 || name != "photos" || host != "/srv/photos" {
		t.Fatalf("imported share = (%d, %q, %q), want (7, photos, /srv/photos)", id, name, host)
	}

	// The override keeps the config-derived id it was keyed by.
	var oName, oHost string
	if err := d.SQL().QueryRowContext(ctx,
		`SELECT name, host_path FROM share_identity_override WHERE share_id = 2`).
		Scan(&oName, &oHost); err != nil {
		t.Fatalf("reading the identity override: %v", err)
	}
	if oName != "renamed" || oHost != "/srv/renamed" {
		t.Fatalf("identity override = (%q, %q)", oName, oHost)
	}

	var trashOn int64
	if err := d.SQL().QueryRowContext(ctx,
		`SELECT enabled FROM share_trash_override WHERE share_id = 2`).
		Scan(&trashOn); err != nil {
		t.Fatalf("reading the trash override: %v", err)
	}
	if trashOn != 1 {
		t.Fatalf("trash override = %d, want on", trashOn)
	}
}

func TestImportRunningJobReadsBackInterruptedWithResults(t *testing.T) {
	dir := rustDir(t)
	if _, err := fromrust.Import(context.Background(), dir, testClock()); err != nil {
		t.Fatalf("Import: %v", err)
	}
	d := openState(t, dir)
	ctx := context.Background()

	// j1 was running when the Rust server stopped, so it is imported as
	// interrupted (state 4), with its prior progress (done 2 of total 4) and
	// its two results still readable.
	var (
		opID, opUser, kind, state, progress, total int64
	)
	if err := d.SQL().QueryRowContext(ctx,
		`SELECT id, user, kind, state, progress, total FROM operation`).
		Scan(&opID, &opUser, &kind, &state, &progress, &total); err != nil {
		t.Fatalf("reading the imported operation: %v", err)
	}
	if opUser != 1 || kind != 0 || state != 4 || progress != 2 || total != 4 {
		t.Fatalf("imported op = (user %d, kind %d, state %d, progress %d, total %d), "+
			"want (1, copy, interrupted, 2, 4)", opUser, kind, state, progress, total)
	}

	rows, err := d.SQL().QueryContext(ctx,
		`SELECT path, ok FROM operation_result WHERE operation = ? ORDER BY idx`, opID)
	if err != nil {
		t.Fatalf("reading the imported results: %v", err)
	}
	defer func() {
		if cerr := rows.Close(); cerr != nil {
			t.Errorf("closing the results: %v", cerr)
		}
	}()
	var got []struct {
		path string
		ok   int64
	}
	for rows.Next() {
		var p string
		var ok int64
		if serr := rows.Scan(&p, &ok); serr != nil {
			t.Fatalf("scanning a result: %v", serr)
		}
		got = append(got, struct {
			path string
			ok   int64
		}{p, ok})
	}
	if len(got) != 2 || got[0].path != "a.txt" || got[0].ok != 1 ||
		got[1].path != "b.txt" || got[1].ok != 0 {
		t.Fatalf("imported results = %+v, want a.txt ok then b.txt failed", got)
	}

	// The job owned by user 99 (which the import refused) is dropped, so there
	// is exactly one operation.
	var n int
	if err := d.SQL().QueryRowContext(ctx, `SELECT count(*) FROM operation`).Scan(&n); err != nil {
		t.Fatalf("counting operations: %v", err)
	}
	if n != 1 {
		t.Fatalf("operations = %d, want 1 (the unknown-owner job was refused)", n)
	}
}

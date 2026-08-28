package state

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
)

// Overrides saved by the administrator.
//
// Stored as one JSON document instead of a column per setting, because adding a
// setting in one place and reading it in another is how a screen ends up editing
// a value nothing consumes. The document is read in full, merged, and written in
// full, so a key unknown to this build survives an edit rather than being
// discarded by it.

// Settings retrieves the stored overrides.
func (d *DB) Settings(ctx context.Context) (map[string]any, error) {
	var raw string
	err := d.f.SQL().QueryRowContext(ctx, sqlReadSettings).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) || raw == "" {
		return map[string]any{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("reading the settings document: %w", err)
	}
	out := map[string]any{}
	if jerr := json.Unmarshal([]byte(raw), &out); jerr != nil {
		return nil, fmt.Errorf("the stored settings document is not a document: %w", jerr)
	}
	return out, nil
}

// MergeSettings writes one section's keys, leaving every other key as it
// was.
//
// Read, merge, write, in one transaction: two administrators saving
// different sections at once would otherwise have the later write drop the
// earlier one's section entirely.
//
// The merge reaches inside the section, and that is load-bearing. A save is
// a patch naming the fields it changes, and replacing the section wholesale
// makes every field the caller did not mention disappear. A caller clearing
// a field sends it explicitly, which is the difference between "set this to
// empty" and "I am not talking about this one".
func (d *DB) MergeSettings(ctx context.Context, section string, value any) error {
	// The first call on a fresh database inserts the singleton row, so this
	// path can grow the file even though every later call updates in place.
	if err := d.f.EnsureWritable(); err != nil {
		return err
	}
	return d.Write(ctx, func(tx *sql.Tx) error {
		merged := map[string]any{}
		var raw string
		switch err := tx.QueryRowContext(ctx, sqlReadSettings).Scan(&raw); {
		case err == nil:
			if raw != "" {
				if jerr := json.Unmarshal([]byte(raw), &merged); jerr != nil {
					return jerr
				}
			}
		case errors.Is(err, sql.ErrNoRows):
			// No document exists yet, so this writes one holding just this
			// section.
		default:
			return err
		}

		merged[section] = mergeKeys(merged[section], value)
		encoded, jerr := json.Marshal(merged)
		if jerr != nil {
			return jerr
		}
		_, eerr := tx.ExecContext(ctx, sqlWriteSettings, string(encoded))
		return eerr
	})
}

// mergeKeys folds a section patch over what is stored, and only when both
// sides are documents. A section stored as something else, or a value that
// is not a document, is replaced: there is nothing to merge into, and
// guessing would leave half of one shape under the other.
func mergeKeys(stored, patch any) any {
	old, oldOK := stored.(map[string]any)
	next, nextOK := patch.(map[string]any)
	if !oldOK || !nextOK {
		return patch
	}
	out := make(map[string]any, len(old)+len(next))
	for k, v := range old {
		out[k] = v
	}
	for k, v := range next {
		out[k] = v
	}
	return out
}

// searchSection is the stored search settings, or an empty document when
// there are none. The section holds values written by different callers at
// different times, so reading it whole before writing either is what keeps
// one from dropping the other.
func searchSection(all map[string]any) map[string]any {
	section, ok := all["search"].(map[string]any)
	if !ok || section == nil {
		return map[string]any{}
	}
	return section
}

// IndexNameEnabled reports whether the name index is enabled. An absent value
// means off, since building one is a deliberate choice somebody must make.
func (d *DB) IndexNameEnabled(ctx context.Context) (bool, error) {
	all, err := d.Settings(ctx)
	if err != nil {
		return false, err
	}
	section, ok := all["search"].(map[string]any)
	if !ok {
		return false, nil
	}
	// A value of the wrong shape reads as off, the same answer as absent.
	enabled, ok := section["name_index_enabled"].(bool)
	return ok && enabled, nil
}

// SetIndexNameEnabled stores that choice, merged into the section rather
// than replacing it, so storing the switch does not drop the measured build
// rate stored beside it.
func (d *DB) SetIndexNameEnabled(ctx context.Context, enabled bool) error {
	all, err := d.Settings(ctx)
	if err != nil {
		return err
	}
	section := searchSection(all)
	section["name_index_enabled"] = enabled
	return d.MergeSettings(ctx, "search", section)
}

// IndexBuildRate is the entries-per-second the last completed build
// measured, or zero when none has completed on this deployment. Zero rather
// than a default, because the caller's fallback is a compiled-in guess and
// it has to be able to say which of the two an operator was shown.
func (d *DB) IndexBuildRate(ctx context.Context) (uint64, error) {
	all, err := d.Settings(ctx)
	if err != nil {
		return 0, err
	}
	section, ok := all["search"].(map[string]any)
	if !ok {
		return 0, nil
	}
	// JSON carries every number as a float, and a value of any other shape
	// reads as absent: a stored rate this build cannot make sense of is the
	// same answer as no build having run.
	rate, ok := section["build_rate"].(float64)
	if !ok || rate <= 0 || rate > float64(math.MaxUint64) {
		return 0, nil
	}
	return uint64(rate), nil
}

// SetIndexBuildRate records what a finished build measured, so the following
// estimate comes from this corpus on this disk instead of an untimed
// constant.
func (d *DB) SetIndexBuildRate(ctx context.Context, rate uint64) error {
	all, err := d.Settings(ctx)
	if err != nil {
		return err
	}
	section := searchSection(all)
	section["build_rate"] = rate
	return d.MergeSettings(ctx, "search", section)
}

// FileBytes is how much disk this database occupies, which is what the
// storage screen reports. Measured from the file rather than counted from
// the rows: a row count is not a size, and the number an administrator needs
// is the one the disk sees.
func (d *DB) FileBytes() (int64, error) {
	info, err := os.Stat(d.f.Path())
	if err != nil {
		return 0, fmt.Errorf("measuring the state database: %w", err)
	}
	return info.Size(), nil
}

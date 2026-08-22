package state

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"math"
	"os"
)

// The administrator's stored overrides.
//
// One JSON document rather than a column per setting, because a setting added
// in one place and read in another is how a screen ends up editing a value
// nothing consumes. The document is read whole, merged, and written whole, so
// a key nobody in this build knows about survives an edit rather than being
// dropped by it.

// Settings reads the stored overrides.
func (d *DB) Settings(ctx context.Context) (map[string]any, error) {
	var raw string
	err := d.SQL().QueryRowContext(ctx, sqlReadSettings).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) || raw == "" {
		return map[string]any{}, nil
	}
	if err != nil {
		return nil, err
	}
	out := map[string]any{}
	if jerr := json.Unmarshal([]byte(raw), &out); jerr != nil {
		return nil, jerr
	}
	return out, nil
}

// MergeSettings writes one section, leaving every other key as it was.
//
// Read, merge, write, in one transaction: two administrators saving different
// sections at once would otherwise have the later write drop the earlier one's
// section entirely.
func (d *DB) MergeSettings(ctx context.Context, section string, value any) error {
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
			// No document yet; this creates one carrying only this section.
		default:
			return err
		}

		merged[section] = value
		encoded, jerr := json.Marshal(merged)
		if jerr != nil {
			return jerr
		}
		_, eerr := tx.ExecContext(ctx, sqlWriteSettings, string(encoded))
		return eerr
	})
}

// ClearSettings removes one section's override, so the value falls back to
// what the config file or the compiled-in default says.
func (d *DB) ClearSettings(ctx context.Context, section string) error {
	return d.Write(ctx, func(tx *sql.Tx) error {
		var raw string
		err := tx.QueryRowContext(ctx, sqlReadSettings).Scan(&raw)
		if errors.Is(err, sql.ErrNoRows) || raw == "" {
			return nil
		}
		if err != nil {
			return err
		}
		merged := map[string]any{}
		if jerr := json.Unmarshal([]byte(raw), &merged); jerr != nil {
			return jerr
		}
		delete(merged, section)
		encoded, jerr := json.Marshal(merged)
		if jerr != nil {
			return jerr
		}
		_, eerr := tx.ExecContext(ctx, sqlWriteSettings, string(encoded))
		return eerr
	})
}

// IndexNameEnabled reports whether the name index is on. Absent means off:
// building one is an act somebody has to choose.
func (d *DB) IndexNameEnabled(ctx context.Context) (bool, error) {
	all, err := d.Settings(ctx)
	if err != nil {
		return false, err
	}
	section, ok := all["search"].(map[string]any)
	if !ok {
		return false, nil
	}
	// A value of the wrong shape reads as off, which is the same answer as
	// absent: turning the index on is an act somebody has to have chosen.
	enabled, ok := section["name_index_enabled"].(bool)
	return ok && enabled, nil
}

// SetIndexNameEnabled stores that choice.
func (d *DB) SetIndexNameEnabled(ctx context.Context, enabled bool) error {
	all, err := d.Settings(ctx)
	if err != nil {
		return err
	}
	section, _ := all["search"].(map[string]any)
	if section == nil {
		section = map[string]any{}
	}
	// Merged into the section rather than replacing it, so storing the switch
	// does not drop the measured build rate stored beside it.
	section["name_index_enabled"] = enabled
	return d.MergeSettings(ctx, "search", section)
}

// IndexBuildRate is the entries-per-second the last completed build measured,
// or zero when none has completed on this deployment.
//
// Zero rather than a default, because the caller's fallback is a compiled-in
// guess and it has to be able to say which of the two an operator was shown.
func (d *DB) IndexBuildRate(ctx context.Context) (uint64, error) {
	all, err := d.Settings(ctx)
	if err != nil {
		return 0, err
	}
	section, ok := all["search"].(map[string]any)
	if !ok {
		return 0, nil
	}
	// JSON carries every number as a float, and a value of any other shape is
	// read as absent: a stored rate this build cannot make sense of is the same
	// answer as no build having run.
	rate, ok := section["build_rate"].(float64)
	if !ok || rate <= 0 || rate > float64(math.MaxUint64) {
		return 0, nil
	}
	return uint64(rate), nil
}

// SetIndexBuildRate stores what a completed build measured, so the next
// estimate is derived from this corpus on this disk rather than from a
// constant nobody timed.
func (d *DB) SetIndexBuildRate(ctx context.Context, rate uint64) error {
	all, err := d.Settings(ctx)
	if err != nil {
		return err
	}
	section, _ := all["search"].(map[string]any)
	if section == nil {
		section = map[string]any{}
	}
	section["build_rate"] = rate
	return d.MergeSettings(ctx, "search", section)
}

// FileBytes is how much disk this database occupies, which is what the storage
// screen reports. Measured from the file rather than counted from the rows: a
// row count is not a size, and the number an administrator needs is the one
// the disk sees.
func (d *DB) FileBytes() (int64, error) {
	info, err := os.Stat(d.File().Path())
	if err != nil {
		return 0, err
	}
	return info.Size(), nil
}

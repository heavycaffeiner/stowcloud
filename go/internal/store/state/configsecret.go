package state

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// The settings that are credentials.
//
// They live apart from the settings document because that document is read
// whole by everything that reads any setting, and is rendered to the settings
// screen. A credential in it would be a credential in all of those. What is
// stored here is already sealed under the master key; this layer holds bytes
// and has no key.

// ConfigSecret is one sealed value and the key version that sealed it.
type ConfigSecret struct {
	Value  []byte
	KeyVer uint32
}

// ReadConfigSecret reads one sealed setting. A name with no row is not an
// error: a deployment that never configured single sign-on has no secret.
func (d *DB) ReadConfigSecret(ctx context.Context, name string) (ConfigSecret, bool, error) {
	var s ConfigSecret
	err := d.f.SQL().QueryRowContext(ctx, sqlReadConfigSecret, name).Scan(&s.Value, &s.KeyVer)
	if errors.Is(err, sql.ErrNoRows) {
		return ConfigSecret{}, false, nil
	}
	if err != nil {
		return ConfigSecret{}, false, fmt.Errorf("reading a configuration secret: %w", err)
	}
	return s, true, nil
}

// WriteConfigSecret stores one sealed setting.
func (d *DB) WriteConfigSecret(ctx context.Context, name string, s ConfigSecret) error {
	if err := d.f.EnsureWritable(); err != nil {
		return err
	}
	return d.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, sqlWriteConfigSecret, name, s.Value, s.KeyVer)
		return err
	})
}

// DeleteConfigSecret removes one, which is what clearing the setting means.
func (d *DB) DeleteConfigSecret(ctx context.Context, name string) error {
	return d.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, sqlDeleteConfigSecret, name)
		return err
	})
}

// Every statement this file runs, as a constant (D14).
const (
	sqlReadConfigSecret = `SELECT value, key_ver FROM config_secret WHERE name = ?`

	sqlWriteConfigSecret = `
INSERT INTO config_secret(name, value, key_ver) VALUES (?, ?, ?)
ON CONFLICT(name) DO UPDATE SET value = excluded.value, key_ver = excluded.key_ver`

	sqlDeleteConfigSecret = `DELETE FROM config_secret WHERE name = ?`
)

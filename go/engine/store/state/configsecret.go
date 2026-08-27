package state

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/heavycaffeiner/stowcloud/go/engine/kit/num"
)

// The settings that are credentials.
//
// They live apart from the settings document because that document is read
// whole by everything that reads any setting, and is rendered to the
// settings screen. A credential in it would be a credential in all of those.
// What is stored here is already sealed under the master key; this layer
// holds bytes and a key version and has no key of its own.

// ConfigSecret is one sealed value and the key version that sealed it.
type ConfigSecret struct {
	Value  []byte
	KeyVer uint32
}

// ReadConfigSecret reads one sealed setting. A name with no row is not an
// error: a deployment that never configured single sign-on has no secret.
func (d *DB) ReadConfigSecret(ctx context.Context, name string) (ConfigSecret, bool, error) {
	var (
		s   ConfigSecret
		ver int64
	)
	err := d.f.SQL().QueryRowContext(ctx, sqlReadConfigSecret, name).Scan(&s.Value, &ver)
	if errors.Is(err, sql.ErrNoRows) {
		return ConfigSecret{}, false, nil
	}
	if err != nil {
		return ConfigSecret{}, false, fmt.Errorf("reading a configuration secret: %w", err)
	}
	v, err := num.Narrow[uint32](ver)
	if err != nil {
		return ConfigSecret{}, false, fmt.Errorf(
			"the configuration secret %q carries key version %d: %w", name, ver, err)
	}
	s.KeyVer = v
	return s, true, nil
}

// WriteConfigSecret stores one sealed setting.
func (d *DB) WriteConfigSecret(ctx context.Context, name string, s ConfigSecret) error {
	if err := d.f.EnsureWritable(); err != nil {
		return err
	}
	return d.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, sqlWriteConfigSecret, name, s.Value, int64(s.KeyVer))
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

package state

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/heavycaffeiner/stowcloud/go/engine/kit/num"
)

// Settings that happen to be credentials.
//
// They sit outside the settings document because anything reading any setting
// reads that document in full, and it is rendered onto the settings screen. A
// credential placed there would be exposed through all of that. Values stored
// here arrive already sealed under the master key; this layer handles bytes and
// a key version while holding no key itself.

// ConfigSecret pairs a sealed value with the key version that sealed it.
type ConfigSecret struct {
	Value  []byte
	KeyVer uint32
}

// ReadConfigSecret retrieves a sealed setting. A name lacking a row is not an
// error, since a deployment that never set up single sign-on holds no secret.
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

// WriteConfigSecret persists a sealed setting.
func (d *DB) WriteConfigSecret(ctx context.Context, name string, s ConfigSecret) error {
	if err := d.f.EnsureWritable(); err != nil {
		return err
	}
	return d.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, sqlWriteConfigSecret, name, s.Value, int64(s.KeyVer))
		return err
	})
}

// DeleteConfigSecret removes one, which is what clearing the setting amounts
// to.
func (d *DB) DeleteConfigSecret(ctx context.Context, name string) error {
	return d.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, sqlDeleteConfigSecret, name)
		return err
	})
}

//go:build linux

// Deployment identity and the compatibility layer's key-value rows.
//
// Both live in one table because they are the same kind of fact: durable,
// server-owned, and read by name. The identity is minted once and never
// regenerated, because a client that saw one value and later a different one
// treats the server as a different server and re-syncs everything it holds.
package state

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
)

// ErrNoCompatKey reports a key nothing has stored yet.
var ErrNoCompatKey = errors.New("no such compatibility key")

// CompatKey reads one value the compatibility layer owns.
func (d *DB) CompatKey(ctx context.Context, key string) (string, error) {
	var v string
	err := d.f.SQL().QueryRowContext(ctx,
		`SELECT value FROM compat_kv WHERE key = ?`, key).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrNoCompatKey
	}
	if err != nil {
		return "", fmt.Errorf("reading the compatibility key %q: %w", key, err)
	}
	return v, nil
}

// PutCompatKey stores one value under one key, leaving any existing value
// alone.
//
// It reports whether it wrote. The caller that mints an identity needs to
// know: two processes racing on first boot must agree on whichever landed,
// and each keeping its own would split the deployment in two.
func (d *DB) PutCompatKeyIfAbsent(ctx context.Context, key, value string) (bool, error) {
	var wrote bool
	err := d.Write(ctx, func(tx *sql.Tx) error {
		res, execErr := tx.ExecContext(ctx,
			`INSERT INTO compat_kv(key, value) VALUES (?, ?)
			 ON CONFLICT(key) DO NOTHING`, key, value)
		if execErr != nil {
			return execErr
		}
		n, cntErr := res.RowsAffected()
		if cntErr != nil {
			return cntErr
		}
		wrote = n > 0
		return nil
	})
	if err != nil {
		return false, fmt.Errorf("storing the compatibility key %q: %w", key, err)
	}
	return wrote, nil
}

// InstanceID reads the deployment's identity, producing one if the database
// has none. Every caller after the first gets what the first stored.
const instanceIDKey = "instance_id"

// instanceIDBytes matches what the deployment's identity has always been:
// sixteen bytes, hex-encoded. The shape is stated here rather than imported,
// because the tier rule leaves the store with no route to the core.
const instanceIDBytes = 16

func mintInstanceID() (string, error) {
	buf := make([]byte, instanceIDBytes)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("minting the instance id: %w", err)
	}
	return hex.EncodeToString(buf), nil
}

func (d *DB) InstanceID(ctx context.Context) (string, error) {
	id, err := d.CompatKey(ctx, instanceIDKey)
	if err == nil {
		return id, nil
	}
	if !errors.Is(err, ErrNoCompatKey) {
		return "", err
	}
	minted, merr := mintInstanceID()
	if merr != nil {
		return "", merr
	}
	// The write is conditional, so two processes racing on first boot end on
	// whichever row landed first rather than each keeping its own mint. The
	// read-back is what makes the winner's value the one this caller reports
	// too, rather than the loser keeping a secret second identity.
	if _, werr := d.PutCompatKeyIfAbsent(ctx, instanceIDKey, minted); werr != nil {
		return "", werr
	}
	return d.CompatKey(ctx, instanceIDKey)
}

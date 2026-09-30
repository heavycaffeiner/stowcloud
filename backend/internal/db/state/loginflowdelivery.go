package state

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"
)

// The rejections delivery produces.
var (
	// ErrLoginFlowNotApproved reports a poll on a flow nobody approved yet.
	ErrLoginFlowNotApproved = errors.New("the login flow is not approved")
	// ErrLoginFlowClaimed reports a delivery another poll already claimed.
	ErrLoginFlowClaimed = errors.New("the login flow delivery is already claimed")
)

// LoginFlowDelivery is a flow's delivery state.
type LoginFlowDelivery struct {
	// ClaimedNs is when a poll took responsibility for minting, or zero.
	ClaimedNs int64
	// Sealed is the credential sealed under the master key, or nil.
	//
	// Sealed rather than stored plaintext: this table records which sign-ins
	// are underway, and a plaintext password here would make reading it enough
	// to become the user.
	Sealed []byte
	// SealedKeyVer is the key version Sealed was made under, so a rotation
	// does not strand a flow that is already deliverable.
	SealedKeyVer uint32
	// CredentialID identifies the app password, for audit and for revoking it
	// if sealing fails. Never its value.
	CredentialID *int64
	// DeliveredNs is when a response was first written, or zero.
	DeliveredNs int64
}

// HasResult reports whether a credential is sealed and collectable.
func (d LoginFlowDelivery) HasResult() bool { return len(d.Sealed) > 0 }

// ClaimLoginFlowDelivery takes responsibility for minting exactly once.
//
// The guard is inside the statement rather than a read followed by a write.
// Two approved polls arriving together would both pass a check made before
// either wrote, and the client would then hold two credentials with no way to
// know the second exists.
func (d *DB) ClaimLoginFlowDelivery(ctx context.Context, pollDigest []byte, nowNs int64) error {
	if err := d.f.EnsureWritable(); err != nil {
		return err
	}

	return d.Write(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, sqlClaimLoginFlowDelivery, nowNs, pollDigest)
		if err != nil {
			return fmt.Errorf("claiming a login flow delivery: %w", err)
		}
		n, err := res.RowsAffected()
		if err != nil {
			return fmt.Errorf("claiming a login flow delivery: %w", err)
		}
		if n == 1 {
			return nil
		}

		// Nothing changed, so the row is missing, unapproved or already
		// claimed. A caller answers each differently, so they are separated
		// here rather than collapsed into one failure.
		return unclaimedReason(ctx, tx, pollDigest)
	})
}

// unclaimedReason explains why a claim changed no row.
func unclaimedReason(ctx context.Context, tx *sql.Tx, pollDigest []byte) error {
	var (
		claimed   int64
		sealed    []byte
		keyVer    int64
		credID    sql.NullInt64
		delivered int64
	)
	err := tx.QueryRowContext(ctx, sqlSelectLoginFlowDelivery, pollDigest).
		Scan(&claimed, &sealed, &keyVer, &credID, &delivered)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrLoginFlowUnknown
	}
	if err != nil {
		return fmt.Errorf("reading a login flow delivery: %w", err)
	}
	if claimed != 0 {
		return ErrLoginFlowClaimed
	}
	return ErrLoginFlowNotApproved
}

// StoreLoginFlowDelivery records the sealed credential.
func (d *DB) StoreLoginFlowDelivery(
	ctx context.Context, pollDigest, sealed []byte, keyVer uint32, credentialID int64,
) error {
	if err := d.f.EnsureWritable(); err != nil {
		return err
	}

	return d.Write(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, sqlStoreLoginFlowDelivery,
			sealed, int64(keyVer), credentialID, pollDigest)
		if err != nil {
			return fmt.Errorf("storing a login flow delivery: %w", err)
		}
		n, err := res.RowsAffected()
		if err != nil {
			return fmt.Errorf("storing a login flow delivery: %w", err)
		}
		if n == 0 {
			return ErrLoginFlowUnknown
		}
		return nil
	})
}

// LoginFlowDeliveryState reads a flow's delivery state.
func (d *DB) LoginFlowDeliveryState(ctx context.Context, pollDigest []byte) (LoginFlowDelivery, error) {
	var (
		out    LoginFlowDelivery
		keyVer int64
		credID sql.NullInt64
	)
	err := d.f.SQL().QueryRowContext(ctx, sqlSelectLoginFlowDelivery, pollDigest).
		Scan(&out.ClaimedNs, &out.Sealed, &keyVer, &credID, &out.DeliveredNs)
	if errors.Is(err, sql.ErrNoRows) {
		return LoginFlowDelivery{}, ErrLoginFlowUnknown
	}
	if err != nil {
		return LoginFlowDelivery{}, fmt.Errorf("reading a login flow delivery: %w", err)
	}

	// A key version outside the range one could have been written in means the
	// row is not one this build wrote. Refusing beats truncating: a truncated
	// version names a different key, and opening the ciphertext under it fails
	// in a way nothing explains.
	if keyVer < 0 || keyVer > math.MaxUint32 {
		return LoginFlowDelivery{}, fmt.Errorf("%w: an unusable key version", ErrLoginFlowUnknown)
	}
	out.SealedKeyVer = uint32(keyVer)
	if credID.Valid {
		id := credID.Int64
		out.CredentialID = &id
	}
	return out, nil
}

// MarkLoginFlowDelivered records that a response was written.
//
// The sealed result stays. A connection lost after the server wrote its
// response is exactly the case redelivery exists for, and clearing here would
// make the client's retry mint a second credential.
func (d *DB) MarkLoginFlowDelivered(ctx context.Context, pollDigest []byte, nowNs int64) error {
	return d.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, sqlMarkLoginFlowDelivered, nowNs, pollDigest)
		if err != nil {
			return fmt.Errorf("marking a login flow delivered: %w", err)
		}
		return nil
	})
}

// SweepLoginFlowMaterial clears temporary ciphertext past the cutoff.
//
// Only the ciphertext. The app password the client now owns is not touched:
// this removes the server's copy of a secret the client already has, and
// deleting the credential would sign the user out of a working client.
func (d *DB) SweepLoginFlowMaterial(ctx context.Context, cutoffNs int64) (int64, error) {
	var n int64
	err := d.Write(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, sqlClearLoginFlowMaterial, cutoffNs)
		if err != nil {
			return fmt.Errorf("clearing login flow material: %w", err)
		}
		n, err = res.RowsAffected()
		return err
	})
	return n, err
}

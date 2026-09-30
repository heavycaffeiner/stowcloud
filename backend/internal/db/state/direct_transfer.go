package state

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"strings"
)

// DirectTransferState is the durable reservation lifecycle.
type DirectTransferState int8

const (
	DirectTransferPending DirectTransferState = iota
	DirectTransferCompleting
	DirectTransferComplete
	DirectTransferCancelled
	DirectTransferExpired
)

func (s DirectTransferState) String() string {
	switch s {
	case DirectTransferPending:
		return "pending"
	case DirectTransferCompleting:
		return "completing"
	case DirectTransferComplete:
		return "complete"
	case DirectTransferCancelled:
		return "cancelled"
	case DirectTransferExpired:
		return "expired"
	default:
		return "unknown"
	}
}

// DirectTransferPartState is a part's durable lifecycle.
type DirectTransferPartState int8

const (
	DirectTransferPartPending DirectTransferPartState = iota
	DirectTransferPartUploaded
	DirectTransferPartComplete
)

// DirectTransferReservation is one persisted multipart reservation. ID is an
// opaque, caller-generated token and is never interpreted as an object key.
type DirectTransferReservation struct {
	ID               string
	Owner            int64
	Share            int64
	Path             string
	ObjectKey        string
	UploadID         string
	ExpectedSize     uint64
	ExpectedChecksum string
	IfMatch          string
	PriorSize        uint64
	PriorETag        string
	ConflictPolicy   string
	QuotaReservation uint64
	CreatedNs        int64
	UpdatedNs        int64
	ExpiresNs        int64
	State            DirectTransferState
	ErrorKey         string
	ErrorDetail      string
	CompletedNs      *int64
	LeaseID          string
	LeaseExpiresNs   int64
	QuotaReleased    bool
	ReceiptRecorded  bool
	ReceiptSize      uint64
	ReceiptETag      string
	ReceiptChecksum  string
}

// DirectTransferPart is one recorded multipart part. Owner is optional for
// callers using the reservation-scoped method; owner-scoped writes should set it.
type DirectTransferPart struct {
	TransferID     string
	Owner          int64
	PartNumber     int64
	ETag           string
	Size           uint64
	Checksum       string
	UploadedNs     int64
	State          DirectTransferPartState
	LeaseID        string
	LeaseExpiresNs int64
}

var (
	ErrNoSuchDirectTransfer   = errors.New("no such direct transfer")
	ErrDirectTransferConflict = errors.New("direct transfer is not owned or has an invalid state")
	ErrDirectTransferExpired  = errors.New("direct transfer has expired")
)

func validateTransferID(id string) error {
	if id == "" || len(id) > 512 || strings.IndexByte(id, 0) >= 0 {
		return fmt.Errorf("invalid direct transfer id")
	}
	return nil
}

func narrowTransferSize(v uint64) (int64, error) {
	if v > math.MaxInt64 {
		return 0, fmt.Errorf("direct transfer size %d exceeds SQLite integer range", v)
	}
	return int64(v), nil
}

// CreateDirectTransfer persists a pending reservation.
func (d *DB) CreateDirectTransfer(ctx context.Context, r DirectTransferReservation) error {
	if err := validateTransferID(r.ID); err != nil {
		return err
	}
	if r.Owner <= 0 || r.Share <= 0 || r.Path == "" || r.ObjectKey == "" || r.UploadID == "" || r.ExpiresNs <= 0 {
		return fmt.Errorf("invalid direct transfer reservation")
	}
	size, err := narrowTransferSize(r.ExpectedSize)
	if err != nil {
		return err
	}
	prior, err := narrowTransferSize(r.PriorSize)
	if err != nil {
		return err
	}
	quota, err := narrowTransferSize(r.QuotaReservation)
	if err != nil {
		return err
	}
	if r.State != DirectTransferPending {
		return ErrDirectTransferConflict
	}
	if err := d.f.EnsureWritable(); err != nil {
		return err
	}
	return d.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, sqlInsertDirectTransfer, r.ID, r.Owner, r.Share, r.Path, r.ObjectKey, r.UploadID,
			size, textArg(r.ExpectedChecksum), textArg(r.IfMatch), prior, textArg(r.PriorETag), r.ConflictPolicy, quota,
			r.CreatedNs, r.UpdatedNs, r.ExpiresNs, int64(r.State), textArg(r.ErrorKey), textArg(r.ErrorDetail), r.CompletedNs,
			textArg(r.LeaseID), r.LeaseExpiresNs, r.QuotaReleased, r.ReceiptRecorded, r.ReceiptSize, textArg(r.ReceiptETag), textArg(r.ReceiptChecksum))
		return err
	})
}

// GetDirectTransfer retrieves a reservation by opaque id. Use
// GetDirectTransferOf for an owner-scoped lookup at a trust boundary.
func (d *DB) GetDirectTransfer(ctx context.Context, id string) (DirectTransferReservation, error) {
	return d.getDirectTransfer(ctx, id, 0)
}

// GetDirectTransferOf retrieves only a reservation owned by owner.
func (d *DB) GetDirectTransferOf(ctx context.Context, owner int64, id string) (DirectTransferReservation, error) {
	return d.getDirectTransfer(ctx, id, owner)
}

func (d *DB) getDirectTransfer(ctx context.Context, id string, owner int64) (DirectTransferReservation, error) {
	if err := validateTransferID(id); err != nil {
		return DirectTransferReservation{}, ErrNoSuchDirectTransfer
	}
	row := d.f.SQL().QueryRowContext(ctx, sqlReadDirectTransfer, id, owner, owner)
	var r DirectTransferReservation
	var expected, prior, quota, receiptSize int64
	var checksum, ifMatch, priorETag, policy, errorKey, errorDetail, leaseID, receiptETag, receiptChecksum sql.NullString
	var completed sql.NullInt64
	if err := row.Scan(&r.ID, &r.Owner, &r.Share, &r.Path, &r.ObjectKey, &r.UploadID, &expected, &checksum, &ifMatch,
		&prior, &priorETag, &policy, &quota, &r.CreatedNs, &r.UpdatedNs, &r.ExpiresNs, &r.State, &errorKey, &errorDetail,
		&completed, &leaseID, &r.LeaseExpiresNs, &r.QuotaReleased, &r.ReceiptRecorded, &receiptSize, &receiptETag, &receiptChecksum); errors.Is(err, sql.ErrNoRows) {
		return DirectTransferReservation{}, ErrNoSuchDirectTransfer
	} else if err != nil {
		return DirectTransferReservation{}, fmt.Errorf("reading direct transfer: %w", err)
	}
	if expected < 0 || prior < 0 || quota < 0 || receiptSize < 0 {
		return DirectTransferReservation{}, fmt.Errorf("direct transfer has a negative persisted size")
	}
	r.ExpectedSize, r.PriorSize, r.QuotaReservation, r.ReceiptSize = uint64(expected), uint64(prior), uint64(quota), uint64(receiptSize)
	r.ExpectedChecksum, r.IfMatch, r.PriorETag, r.ConflictPolicy = checksum.String, ifMatch.String, priorETag.String, policy.String
	r.ErrorKey, r.ErrorDetail, r.LeaseID, r.ReceiptETag, r.ReceiptChecksum, r.CompletedNs = errorKey.String, errorDetail.String, leaseID.String, receiptETag.String, receiptChecksum.String, nil
	if completed.Valid {
		v := completed.Int64
		r.CompletedNs = &v
	}
	return r, nil
}

// PutDirectTransferPart records or replaces one uploaded part for its pending reservation.
func (d *DB) PutDirectTransferPart(ctx context.Context, p DirectTransferPart) error {
	if err := validateTransferID(p.TransferID); err != nil || p.PartNumber <= 0 || p.ETag == "" {
		return ErrDirectTransferConflict
	}
	size, err := narrowTransferSize(p.Size)
	if err != nil {
		return err
	}
	if p.State != DirectTransferPartPending && p.State != DirectTransferPartUploaded && p.State != DirectTransferPartComplete {
		return ErrDirectTransferConflict
	}
	return d.Write(ctx, func(tx *sql.Tx) error {
		var owner int64
		if err := tx.QueryRowContext(ctx, `SELECT owner FROM direct_transfer WHERE id = ? AND (? = 0 OR owner = ?) AND state = ? AND expires_ns > updated_ns`, p.TransferID, p.Owner, p.Owner, int64(DirectTransferPending)).Scan(&owner); errors.Is(err, sql.ErrNoRows) {
			return ErrNoSuchDirectTransfer
		} else if err != nil {
			return err
		}
		if p.Owner != 0 && owner != p.Owner {
			return ErrDirectTransferConflict
		}
		_, err := tx.ExecContext(ctx, sqlUpsertDirectTransferPart, p.TransferID, p.PartNumber, p.ETag, size, textArg(p.Checksum), p.UploadedNs, int64(p.State), textArg(p.LeaseID), p.LeaseExpiresNs)
		return err
	})
}

// PutDirectTransferPartOf is an owner-scoped variant for HTTP handlers.
func (d *DB) PutDirectTransferPartOf(ctx context.Context, owner int64, p DirectTransferPart) error {
	p.Owner = owner
	return d.PutDirectTransferPart(ctx, p)
}

// ListDirectTransferParts retrieves all recorded parts in ascending part order.
func (d *DB) ListDirectTransferParts(ctx context.Context, id string) (out []DirectTransferPart, err error) {
	if verr := validateTransferID(id); verr != nil {
		return nil, ErrNoSuchDirectTransfer
	}
	rows, err := d.f.SQL().QueryContext(ctx, sqlListDirectTransferParts, id)
	if err != nil {
		return nil, fmt.Errorf("listing direct transfer parts: %w", err)
	}
	defer func() { err = errors.Join(err, rows.Close()) }()
	for rows.Next() {
		var p DirectTransferPart
		var size int64
		var checksum, lease sql.NullString
		if err := rows.Scan(&p.TransferID, &p.PartNumber, &p.ETag, &size, &checksum, &p.UploadedNs, &p.State, &lease, &p.LeaseExpiresNs); err != nil {
			return nil, err
		}
		if size < 0 {
			return nil, fmt.Errorf("direct transfer part has negative size")
		}
		p.Size, p.Checksum, p.LeaseID = uint64(size), checksum.String, lease.String
		out = append(out, p)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

// BeginDirectTransferCompletion durably records that provider publication is
// about to start. Completing rows are never eligible for expiry cleanup.
func (d *DB) BeginDirectTransferCompletion(ctx context.Context, id string, owner int64, nowNs int64) error {
	return d.Write(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `UPDATE direct_transfer SET state=?,updated_ns=? WHERE id=? AND owner=? AND state=? AND expires_ns > ? AND NOT EXISTS (SELECT 1 FROM direct_transfer AS other WHERE other.share=direct_transfer.share AND other.path=direct_transfer.path AND other.state=?)`, int64(DirectTransferCompleting), nowNs, id, owner, int64(DirectTransferPending), nowNs, int64(DirectTransferCompleting))
		if err != nil {
			return err
		}
		n, err := res.RowsAffected()
		if err != nil {
			return err
		}
		if n == 1 {
			return nil
		}
		var expires int64
		if err := tx.QueryRowContext(ctx, `SELECT expires_ns FROM direct_transfer WHERE id=? AND owner=? AND state=?`, id, owner, int64(DirectTransferPending)).Scan(&expires); errors.Is(err, sql.ErrNoRows) {
			return directTransferMutationFailure(ctx, tx, id, owner)
		} else if err != nil {
			return err
		}
		if expires <= nowNs {
			return ErrDirectTransferExpired
		}
		return ErrDirectTransferConflict
	})
}

// ResetDirectTransferCompletion makes an unresolved provider failure retryable.
func (d *DB) ResetDirectTransferCompletion(ctx context.Context, id string, owner int64, nowNs int64) error {
	return d.Write(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `UPDATE direct_transfer SET state=?,updated_ns=? WHERE id=? AND owner=? AND state=?`, int64(DirectTransferPending), nowNs, id, owner, int64(DirectTransferCompleting))
		if err != nil {
			return err
		}
		n, err := res.RowsAffected()
		if err != nil {
			return err
		}
		if n == 1 {
			return nil
		}
		return directTransferMutationFailure(ctx, tx, id, owner)
	})
}

// PublishDirectTransfer records the provider receipt and publishes a completing row.
func (d *DB) PublishDirectTransfer(ctx context.Context, id string, owner int64, receiptSize uint64, receiptETag, receiptChecksum string, completedNs int64) error {
	size, err := narrowTransferSize(receiptSize)
	if err != nil {
		return err
	}
	return d.Write(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `UPDATE direct_transfer SET state=?,updated_ns=?,completed_ns=?,receipt_recorded=1,receipt_size=?,receipt_etag=?,receipt_checksum=?,error_key=NULL,error_detail=NULL,lease_id=NULL,lease_expires_ns=0 WHERE id=? AND owner=? AND state=?`, int64(DirectTransferComplete), completedNs, completedNs, size, textArg(receiptETag), textArg(receiptChecksum), id, owner, int64(DirectTransferCompleting))
		if err != nil {
			return err
		}
		n, err := res.RowsAffected()
		if err != nil {
			return err
		}
		if n == 1 {
			return nil
		}
		return directTransferMutationFailure(ctx, tx, id, owner)
	})
}

// PublishDirectTransferAndReleaseUsage records the provider receipt and credits
// the replacement bytes in one transaction.
func (d *DB) PublishDirectTransferAndReleaseUsage(ctx context.Context, id string, owner int64, receiptSize uint64, receiptETag, receiptChecksum string, completedNs int64, release uint64) error {
	size, err := narrowTransferSize(receiptSize)
	if err != nil {
		return err
	}
	delta, err := narrowTransferSize(release)
	if err != nil {
		return err
	}
	return d.Write(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `UPDATE direct_transfer SET state=?,updated_ns=?,completed_ns=?,receipt_recorded=1,receipt_size=?,receipt_etag=?,receipt_checksum=?,error_key=NULL,error_detail=NULL,lease_id=NULL,lease_expires_ns=0,quota_released=1 WHERE id=? AND owner=? AND state=? AND quota_released=0`, int64(DirectTransferComplete), completedNs, completedNs, size, textArg(receiptETag), textArg(receiptChecksum), id, owner, int64(DirectTransferCompleting))
		if err != nil {
			return err
		}
		n, err := res.RowsAffected()
		if err != nil {
			return err
		}
		if n != 1 {
			return directTransferMutationFailure(ctx, tx, id, owner)
		}
		_, err = tx.ExecContext(ctx, sqlReleaseQuota, delta, owner)
		return err
	})
}

// CancelDirectTransferAndReleaseUsage atomically terminates a pending row and
// credits its reservation before the provider abort is attempted.
func (d *DB) CancelDirectTransferAndReleaseUsage(ctx context.Context, id string, owner int64, nowNs int64, release uint64) error {
	delta, err := narrowTransferSize(release)
	if err != nil {
		return err
	}
	return d.Write(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `UPDATE direct_transfer SET state=?,updated_ns=?,error_key='cancelled',error_detail='cancelled by owner',completed_ns=?,lease_id=NULL,lease_expires_ns=0,quota_released=1 WHERE id=? AND owner=? AND state=? AND quota_released=0`, int64(DirectTransferCancelled), nowNs, nowNs, id, owner, int64(DirectTransferPending))
		if err != nil {
			return err
		}
		n, err := res.RowsAffected()
		if err != nil {
			return err
		}
		if n != 1 {
			return directTransferMutationFailure(ctx, tx, id, owner)
		}
		_, err = tx.ExecContext(ctx, sqlReleaseQuota, delta, owner)
		return err
	})
}

// ExpireDirectTransferAndReleaseUsage atomically expires a row and credits its reservation.
func (d *DB) ExpireDirectTransferAndReleaseUsage(ctx context.Context, id string, nowNs int64, owner int64, release uint64) error {
	delta, err := narrowTransferSize(release)
	if err != nil {
		return err
	}
	return d.Write(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `UPDATE direct_transfer SET state=?,updated_ns=?,error_key='expired',error_detail='reservation expired',lease_id=NULL,lease_expires_ns=0,quota_released=1 WHERE id=? AND owner=? AND state=? AND expires_ns <= ? AND quota_released=0`, int64(DirectTransferExpired), nowNs, id, owner, int64(DirectTransferPending), nowNs)
		if err != nil {
			return err
		}
		n, err := res.RowsAffected()
		if err != nil {
			return err
		}
		if n != 1 {
			return directTransferMutationFailure(ctx, tx, id, owner)
		}
		_, err = tx.ExecContext(ctx, sqlReleaseQuota, delta, owner)
		return err
	})
}

// CompleteDirectTransfer conditionally terminates a pending reservation.
func (d *DB) CompleteDirectTransfer(ctx context.Context, id string, owner int64, state DirectTransferState, errorKey, errorDetail string, completedNs int64) error {
	if state != DirectTransferCancelled && state != DirectTransferExpired {
		return ErrDirectTransferConflict
	}
	return d.Write(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `UPDATE direct_transfer SET state = ?, updated_ns = ?, error_key = ?, error_detail = ?, completed_ns = ?, lease_id = NULL, lease_expires_ns = 0 WHERE id = ? AND owner = ? AND state = ? AND expires_ns > ?`, int64(state), completedNs, textArg(errorKey), textArg(errorDetail), completedNs, id, owner, int64(DirectTransferPending), completedNs)
		if err != nil {
			return err
		}
		n, err := res.RowsAffected()
		if err != nil {
			return err
		}
		if n == 1 {
			return nil
		}
		return directTransferMutationFailure(ctx, tx, id, owner)
	})
}

// CancelDirectTransfer cancels a pending reservation owned by owner.
func (d *DB) CancelDirectTransfer(ctx context.Context, id string, owner int64, nowNs int64) error {
	return d.CompleteDirectTransfer(ctx, id, owner, DirectTransferCancelled, "cancelled", "cancelled by owner", nowNs)
}

// ListExpiredDirectTransfers lists reservations requiring object-store abort.
func (d *DB) ListExpiredDirectTransfers(ctx context.Context, nowNs int64, limit int) (out []DirectTransferReservation, err error) {
	if limit <= 0 {
		limit = 100
	}
	rows, err := d.f.SQL().QueryContext(ctx, sqlListExpiredDirectTransfers, int64(DirectTransferPending), nowNs, limit)
	if err != nil {
		return nil, err
	}
	defer func() { err = errors.Join(err, rows.Close()) }()
	for rows.Next() {
		var r DirectTransferReservation
		var expected, prior, quota, receiptSize int64
		var checksum, ifMatch, priorETag, policy, errorKey, errorDetail, lease, receiptETag, receiptChecksum sql.NullString
		var completed sql.NullInt64
		if err := rows.Scan(&r.ID, &r.Owner, &r.Share, &r.Path, &r.ObjectKey, &r.UploadID, &expected, &checksum, &ifMatch, &prior, &priorETag, &policy, &quota, &r.CreatedNs, &r.UpdatedNs, &r.ExpiresNs, &r.State, &errorKey, &errorDetail, &completed, &lease, &r.LeaseExpiresNs, &r.QuotaReleased, &r.ReceiptRecorded, &receiptSize, &receiptETag, &receiptChecksum); err != nil {
			return nil, err
		}
		if expected < 0 || prior < 0 || quota < 0 || receiptSize < 0 {
			return nil, fmt.Errorf("direct transfer has a negative persisted size")
		}
		r.ExpectedSize, r.PriorSize, r.QuotaReservation, r.ReceiptSize = uint64(expected), uint64(prior), uint64(quota), uint64(receiptSize)
		r.ExpectedChecksum, r.IfMatch, r.PriorETag, r.ConflictPolicy = checksum.String, ifMatch.String, priorETag.String, policy.String
		r.ErrorKey, r.ErrorDetail, r.LeaseID, r.ReceiptETag, r.ReceiptChecksum = errorKey.String, errorDetail.String, lease.String, receiptETag.String, receiptChecksum.String
		if completed.Valid {
			v := completed.Int64
			r.CompletedNs = &v
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

// ListCompletingDirectTransfers returns rows whose provider publication may
// have committed while the process was unavailable.
func (d *DB) ListCompletingDirectTransfers(ctx context.Context, limit int) (out []DirectTransferReservation, err error) {
	if limit <= 0 {
		limit = 100
	}
	rows, err := d.f.SQL().QueryContext(ctx, sqlListCompletingDirectTransfers, int64(DirectTransferCompleting), limit)
	if err != nil {
		return nil, err
	}
	defer func() { err = errors.Join(err, rows.Close()) }()
	for rows.Next() {
		var r DirectTransferReservation
		var expected, prior, quota, receiptSize int64
		var checksum, ifMatch, priorETag, policy, errorKey, errorDetail, lease, receiptETag, receiptChecksum sql.NullString
		var completed sql.NullInt64
		if err := rows.Scan(&r.ID, &r.Owner, &r.Share, &r.Path, &r.ObjectKey, &r.UploadID, &expected, &checksum, &ifMatch, &prior, &priorETag, &policy, &quota, &r.CreatedNs, &r.UpdatedNs, &r.ExpiresNs, &r.State, &errorKey, &errorDetail, &completed, &lease, &r.LeaseExpiresNs, &r.QuotaReleased, &r.ReceiptRecorded, &receiptSize, &receiptETag, &receiptChecksum); err != nil {
			return nil, err
		}
		if expected < 0 || prior < 0 || quota < 0 || receiptSize < 0 {
			return nil, fmt.Errorf("direct transfer has a negative persisted size")
		}
		r.ExpectedSize, r.PriorSize, r.QuotaReservation, r.ReceiptSize = uint64(expected), uint64(prior), uint64(quota), uint64(receiptSize)
		r.ExpectedChecksum, r.IfMatch, r.PriorETag, r.ConflictPolicy = checksum.String, ifMatch.String, priorETag.String, policy.String
		r.ErrorKey, r.ErrorDetail, r.LeaseID, r.ReceiptETag, r.ReceiptChecksum = errorKey.String, errorDetail.String, lease.String, receiptETag.String, receiptChecksum.String
		if completed.Valid {
			v := completed.Int64
			r.CompletedNs = &v
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

// ExpireDirectTransfer marks one expired reservation and clears its lease.
func (d *DB) ExpireDirectTransfer(ctx context.Context, id string, nowNs int64) error {
	return d.Write(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `UPDATE direct_transfer SET state=?,updated_ns=?,error_key='expired',error_detail='reservation expired',lease_id=NULL,lease_expires_ns=0 WHERE id=? AND state=? AND expires_ns <= ?`, int64(DirectTransferExpired), nowNs, id, int64(DirectTransferPending), nowNs)
		if err != nil {
			return err
		}
		n, err := res.RowsAffected()
		if err != nil {
			return err
		}
		if n == 1 {
			return nil
		}
		return directTransferMutationFailure(ctx, tx, id, 0)
	})
}

// ReleaseDirectTransferQuota atomically returns a reservation's booked bytes once.
func (d *DB) ReleaseDirectTransferQuota(ctx context.Context, id string) (uint64, error) {
	var released int64
	err := d.Write(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `UPDATE direct_transfer SET quota_released=1,updated_ns=updated_ns+1 WHERE id=? AND quota_released=0`, id)
		if err != nil {
			return err
		}
		n, err := res.RowsAffected()
		if err != nil {
			return err
		}
		if n == 0 {
			var q int64
			if err := tx.QueryRowContext(ctx, `SELECT quota_reservation FROM direct_transfer WHERE id=?`, id).Scan(&q); errors.Is(err, sql.ErrNoRows) {
				return ErrNoSuchDirectTransfer
			} else if err != nil {
				return err
			}
			return nil
		}
		return tx.QueryRowContext(ctx, `SELECT quota_reservation FROM direct_transfer WHERE id=?`, id).Scan(&released)
	})
	if err != nil {
		return 0, err
	}
	if released < 0 {
		return 0, fmt.Errorf("negative quota reservation")
	}
	return uint64(released), nil
}

// ReleaseDirectTransferQuotaAndUsage marks the reservation released and credits
// the user's ledger in the same transaction, so a failed credit is retryable.
func (d *DB) ReleaseDirectTransferQuotaAndUsage(ctx context.Context, id string, owner int64, amount uint64) error {
	delta, err := narrowTransferSize(amount)
	if err != nil {
		return err
	}
	return d.Write(ctx, func(tx *sql.Tx) error {
		var rowOwner int64
		var released bool
		scanErr := tx.QueryRowContext(ctx, `SELECT owner,quota_released FROM direct_transfer WHERE id=?`, id).Scan(&rowOwner, &released)
		if errors.Is(scanErr, sql.ErrNoRows) {
			return ErrNoSuchDirectTransfer
		} else if scanErr != nil {
			return scanErr
		}
		if rowOwner != owner {
			return ErrDirectTransferConflict
		}
		if released {
			return nil
		}
		if _, markErr := tx.ExecContext(ctx, `UPDATE direct_transfer SET quota_released=1,updated_ns=updated_ns+1 WHERE id=? AND quota_released=0`, id); markErr != nil {
			return markErr
		}
		_, creditErr := tx.ExecContext(ctx, sqlReleaseQuota, delta, owner)
		return creditErr
	})
}

func directTransferMutationFailure(ctx context.Context, tx *sql.Tx, id string, owner int64) error {
	var n int
	err := tx.QueryRowContext(ctx, `SELECT 1 FROM direct_transfer WHERE id=? AND (?=0 OR owner=?)`, id, owner, owner).Scan(&n)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNoSuchDirectTransfer
	}
	if err != nil {
		return err
	}
	return ErrDirectTransferConflict
}

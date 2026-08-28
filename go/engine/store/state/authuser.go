package state

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/heavycaffeiner/stowcloud/go/engine/kit/num"
	"github.com/heavycaffeiner/stowcloud/go/engine/store/dbfile"
)

// The account directory: the user rows, the groups, and the memberships that
// pair them. One aggregate, because the three are written together on every
// path that changes who exists: creating an account seals its file-sharing
// credential in the same transaction, disabling one drops its sessions, and
// deleting one takes its memberships with it.
//
// The grant aggregate reads memberships for the evaluator's reload; this one
// writes them. The split is by direction of use rather than by table: a
// membership write is an account edit, and a membership read is a permission
// load.

// The refusals this aggregate answers with. Each is a sentinel because the
// service above turns it into a different answer: an absent account is a
// credential failure, a taken name is something the person who typed it can
// fix, and the last administrator is a refusal with a reason.
var (
	// ErrNoSuchAccount is an id or a name that holds no row.
	ErrNoSuchAccount = errors.New("no such account")

	// ErrNoSuchGroup is a group id that holds no row.
	ErrNoSuchGroup = errors.New("no such group")

	// ErrNameTaken is a name another row already holds. It is typed here so
	// no caller has to read a driver's message to find out.
	ErrNameTaken = errors.New("that name is already taken")

	// ErrLastAdmin refuses a write that would leave nobody who can sign in
	// and administer. Recovering from that state means editing the database
	// by hand.
	ErrLastAdmin = errors.New("that would leave no administrator who can sign in")
)

// RoleAdmin is the stored role value of an administrator. Zero is a plain
// account.
const RoleAdmin = 1

// Account is one account row, whole. There is one shape rather than a narrow
// credential row and a wide administrative one, so a surface showing an
// account is looking at the same fields whether it asked for one or for all.
type Account struct {
	ID         int64
	Name       string
	Display    string
	PwHash     string
	Disabled   bool
	Role       int64
	SMBEnabled bool
	SMBOptOut  bool
	CreatedNs  int64
	QuotaBytes *int64
	UsageBytes uint64
	// TOTPEnrolled is read with the row rather than asked for separately,
	// because the login path needs it on every sign-in and the SMB
	// eligibility rule needs it on every publish.
	TOTPEnrolled bool
}

// IsAdmin reports whether the row carries the administrator role.
func (a Account) IsAdmin() bool { return a.Role == RoleAdmin }

// NewAccount is what creating one needs. The SMB credential is not here: it
// is derived from the plaintext by the caller and sealed inside the creating
// transaction, which is the only moment the plaintext exists.
type NewAccount struct {
	Name      string
	Display   string
	PwHash    string
	Role      int64
	CreatedNs int64
}

// AccountByName reads an account by its login name. The name column collates
// case-insensitively, so this is the same lookup the unique index enforces.
func (d *DB) AccountByName(ctx context.Context, name string) (Account, error) {
	return d.account(ctx, sqlAccountByName, name)
}

// AccountByID reads an account by its id.
func (d *DB) AccountByID(ctx context.Context, id int64) (Account, error) {
	return d.account(ctx, sqlAccountByID, id)
}

func (d *DB) account(ctx context.Context, query string, key any) (Account, error) {
	a, err := scanAccount(d.f.SQL().QueryRowContext(ctx, query, key))
	if errors.Is(err, sql.ErrNoRows) {
		return Account{}, ErrNoSuchAccount
	}
	if err != nil {
		return Account{}, fmt.Errorf("reading an account: %w", err)
	}
	return a, nil
}

// ListAccounts returns every account, oldest first, which is the order the
// administrative listing has always shown.
func (d *DB) ListAccounts(ctx context.Context) (out []Account, err error) {
	rows, err := d.f.SQL().QueryContext(ctx, sqlListAccounts)
	if err != nil {
		return nil, fmt.Errorf("listing accounts: %w", err)
	}
	defer func() { err = errors.Join(err, rows.Close()) }()

	for rows.Next() {
		a, serr := scanAccount(rows)
		if serr != nil {
			return nil, fmt.Errorf("reading an account: %w", serr)
		}
		out = append(out, a)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("listing accounts: %w", err)
	}
	return out, nil
}

func scanAccount(row interface{ Scan(...any) error }) (Account, error) {
	var (
		a       Account
		display sql.NullString
		quota   sql.NullInt64
		usage   sql.NullInt64
	)
	if err := row.Scan(&a.ID, &a.Name, &display, &a.PwHash, &a.Disabled, &a.Role,
		&a.SMBEnabled, &a.SMBOptOut, &a.CreatedNs, &quota, &usage, &a.TOTPEnrolled); err != nil {
		return Account{}, err
	}
	// An imported account carries no display name, and reading an absent one
	// into a plain string failed the scan: the account could not log in at
	// all, and the refusal was a server error rather than anything a person
	// could act on.
	a.Display = display.String
	if quota.Valid {
		q := quota.Int64
		a.QuotaBytes = &q
	}
	if usage.Valid && usage.Int64 > 0 {
		u, err := num.Narrow[uint64](usage.Int64)
		if err != nil {
			return Account{}, fmt.Errorf("account %d carries usage %d: %w", a.ID, usage.Int64, err)
		}
		a.UsageBytes = u
	}
	return a, nil
}

// CountAccounts is how many accounts exist, which is the setup gate's "is
// this deployment fresh" question.
func (d *DB) CountAccounts(ctx context.Context) (int64, error) {
	var n int64
	if err := d.f.SQL().QueryRowContext(ctx, sqlCountAccounts).Scan(&n); err != nil {
		return 0, fmt.Errorf("counting accounts: %w", err)
	}
	return n, nil
}

// AdminExists reports whether any administrator account exists. The setup
// gate closes permanently the moment one does, so a token recovered from a
// log or a backup afterwards is worth nothing.
func (d *DB) AdminExists(ctx context.Context) (bool, error) {
	var n int64
	if err := d.f.SQL().QueryRowContext(ctx, sqlCountAdmins).Scan(&n); err != nil {
		return false, fmt.Errorf("counting administrators: %w", err)
	}
	return n > 0, nil
}

// CreateAccount inserts one account and, in the same transaction, the sealed
// SMB credential derived from the password that created it.
//
// sealNT receives the id the row was given, because the seal binds it as
// additional authenticated data. It may be nil for a caller that stores no
// SMB credential. Without this callback the credential would have to be
// written after the transaction, and an account created in the window between
// the two would have a password that works everywhere except the one protocol
// that cannot re-derive it later.
func (d *DB) CreateAccount(
	ctx context.Context, in NewAccount, sealNT func(userID int64) (ct []byte, keyVer uint32, err error),
) (int64, error) {
	if err := d.f.EnsureWritable(); err != nil {
		return 0, err
	}

	var id int64
	err := d.Write(ctx, func(tx *sql.Tx) error {
		res, ierr := tx.ExecContext(ctx, sqlInsertAccount,
			in.Name, textArg(in.Display), in.PwHash, in.Role, in.CreatedNs)
		if ierr != nil {
			if dbfile.IsUniqueViolation(ierr) {
				return ErrNameTaken
			}
			return ierr
		}
		var rerr error
		if id, rerr = res.LastInsertId(); rerr != nil {
			return rerr
		}
		if sealNT == nil {
			return nil
		}
		ct, keyVer, serr := sealNT(id)
		if serr != nil {
			return serr
		}
		_, uerr := tx.ExecContext(ctx, sqlUpsertSMBSecret, id, ct, int64(keyVer))
		return uerr
	})
	if err != nil {
		if errors.Is(err, ErrNameTaken) {
			return 0, err
		}
		return 0, fmt.Errorf("creating an account: %w", err)
	}
	return id, nil
}

// SetAccountPassword replaces the stored hash and the sealed SMB credential
// derived from the same plaintext, in one transaction. The two are one fact
// about the account, and a write that landed only half of them would leave
// SMB answering to the old password.
func (d *DB) SetAccountPassword(
	ctx context.Context, userID int64, pwHash string, ntCT []byte, ntKeyVer uint32,
) error {
	// The SMB row can be an insert for an account that had none.
	if err := d.f.EnsureWritable(); err != nil {
		return err
	}
	err := d.Write(ctx, func(tx *sql.Tx) error {
		res, uerr := tx.ExecContext(ctx, sqlUpdateAccountPassword, pwHash, userID)
		if uerr != nil {
			return uerr
		}
		n, rerr := res.RowsAffected()
		if rerr != nil {
			return rerr
		}
		if n == 0 {
			return ErrNoSuchAccount
		}
		if ntCT == nil {
			return nil
		}
		_, serr := tx.ExecContext(ctx, sqlUpsertSMBSecret, userID, ntCT, int64(ntKeyVer))
		return serr
	})
	if err != nil && !errors.Is(err, ErrNoSuchAccount) {
		return fmt.Errorf("setting an account password: %w", err)
	}
	return err
}

// SetAccountDisabled disables or enables an account. Disabling drops every
// session it holds in the same transaction, and refuses when it would leave
// nobody who can administer the deployment.
func (d *DB) SetAccountDisabled(ctx context.Context, userID int64, disabled bool) error {
	err := d.Write(ctx, func(tx *sql.Tx) error {
		if disabled {
			if gerr := guardLastAdmin(ctx, tx, userID); gerr != nil {
				return gerr
			}
		}
		res, uerr := tx.ExecContext(ctx, sqlUpdateAccountDisabled, disabled, userID)
		if uerr != nil {
			return uerr
		}
		n, rerr := res.RowsAffected()
		if rerr != nil {
			return rerr
		}
		if n == 0 {
			return ErrNoSuchAccount
		}
		if !disabled {
			return nil
		}
		_, derr := tx.ExecContext(ctx, sqlDeleteSessionsOfUser, userID)
		return derr
	})
	if err != nil && !errors.Is(err, ErrNoSuchAccount) && !errors.Is(err, ErrLastAdmin) {
		return fmt.Errorf("changing an account's disabled flag: %w", err)
	}
	return err
}

// DeleteAccount removes an account and everything that referenced it. The
// rows cascade, which is what makes this a deletion rather than an account
// nobody can sign into with its grants still standing.
func (d *DB) DeleteAccount(ctx context.Context, userID int64) error {
	err := d.Write(ctx, func(tx *sql.Tx) error {
		if gerr := guardLastAdmin(ctx, tx, userID); gerr != nil {
			return gerr
		}
		res, derr := tx.ExecContext(ctx, sqlDeleteAccount, userID)
		if derr != nil {
			return derr
		}
		n, rerr := res.RowsAffected()
		if rerr != nil {
			return rerr
		}
		if n == 0 {
			return ErrNoSuchAccount
		}
		return nil
	})
	if err != nil && !errors.Is(err, ErrNoSuchAccount) && !errors.Is(err, ErrLastAdmin) {
		return fmt.Errorf("deleting an account: %w", err)
	}
	return err
}

// SetAccountSMBAccess writes both self-service switches.
//
// They are two facts, not one: opting out says the account holds no
// credential for the protocol at all, and enabled says whether the credential
// it does hold is currently live. Collapsing them into one flag left the
// opt-out column unwritable, so the screen's own toggle never survived a
// reload.
func (d *DB) SetAccountSMBAccess(ctx context.Context, userID int64, optOut, enabled bool) error {
	if err := d.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, sqlUpdateAccountSMBAccess, optOut, enabled, userID)
		return err
	}); err != nil {
		return fmt.Errorf("setting an account's SMB access: %w", err)
	}
	return nil
}

// SetAccountSMBEnabled writes the live half alone, which is what linking and
// unlinking a provider identity moves.
func (d *DB) SetAccountSMBEnabled(ctx context.Context, userID int64, enabled bool) error {
	if err := d.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, sqlUpdateAccountSMBEnabled, enabled, userID)
		return err
	}); err != nil {
		return fmt.Errorf("setting an account's SMB flag: %w", err)
	}
	return nil
}

// SetAccountQuota sets or clears the storage cap. A nil cap is unlimited,
// which is a different fact from a cap of zero.
func (d *DB) SetAccountQuota(ctx context.Context, userID int64, bytes *int64) error {
	if err := d.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, sqlUpdateAccountQuota, idArg(bytes), userID)
		return err
	}); err != nil {
		return fmt.Errorf("setting an account quota: %w", err)
	}
	return nil
}

// guardLastAdmin refuses a write that would leave no administrator who can
// sign in. It runs inside the caller's transaction, so a concurrent disable
// of the other administrator cannot slip between the check and the write.
func guardLastAdmin(ctx context.Context, tx *sql.Tx, userID int64) error {
	var isAdmin int64
	if err := tx.QueryRowContext(ctx, sqlCountThisActiveAdmin, userID).Scan(&isAdmin); err != nil {
		return err
	}
	if isAdmin == 0 {
		return nil
	}
	var others int64
	if err := tx.QueryRowContext(ctx, sqlCountOtherActiveAdmins, userID).Scan(&others); err != nil {
		return err
	}
	if others == 0 {
		return ErrLastAdmin
	}
	return nil
}

// Group is one group row.
type Group struct {
	ID   int64
	Name string
}

// ListGroups returns every group, oldest first.
func (d *DB) ListGroups(ctx context.Context) (out []Group, err error) {
	rows, err := d.f.SQL().QueryContext(ctx, sqlListGroups)
	if err != nil {
		return nil, fmt.Errorf("listing groups: %w", err)
	}
	defer func() { err = errors.Join(err, rows.Close()) }()

	for rows.Next() {
		var g Group
		if serr := rows.Scan(&g.ID, &g.Name); serr != nil {
			return nil, fmt.Errorf("reading a group: %w", serr)
		}
		out = append(out, g)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("listing groups: %w", err)
	}
	return out, nil
}

// GroupByName resolves a group name to its id. Absence is reported rather
// than raised: asking whether a group exists is an ordinary question.
func (d *DB) GroupByName(ctx context.Context, name string) (int64, bool, error) {
	var id int64
	err := d.f.SQL().QueryRowContext(ctx, sqlGroupByName, name).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, fmt.Errorf("resolving a group name: %w", err)
	}
	return id, true, nil
}

// CreateGroup makes a group and returns its id.
func (d *DB) CreateGroup(ctx context.Context, name string) (int64, error) {
	if err := d.f.EnsureWritable(); err != nil {
		return 0, err
	}
	var id int64
	err := d.Write(ctx, func(tx *sql.Tx) error {
		res, ierr := tx.ExecContext(ctx, sqlInsertGroup, name)
		if ierr != nil {
			if dbfile.IsUniqueViolation(ierr) {
				return ErrNameTaken
			}
			return ierr
		}
		var rerr error
		id, rerr = res.LastInsertId()
		return rerr
	})
	if err != nil {
		if errors.Is(err, ErrNameTaken) {
			return 0, err
		}
		return 0, fmt.Errorf("creating a group: %w", err)
	}
	return id, nil
}

// RenameGroup changes a group's name and nothing else. The id is what every
// grant and membership references, so a rename moves the label and touches no
// permission.
func (d *DB) RenameGroup(ctx context.Context, id int64, name string) error {
	err := d.Write(ctx, func(tx *sql.Tx) error {
		res, uerr := tx.ExecContext(ctx, sqlRenameGroup, name, id)
		if uerr != nil {
			if dbfile.IsUniqueViolation(uerr) {
				return ErrNameTaken
			}
			return uerr
		}
		n, rerr := res.RowsAffected()
		if rerr != nil {
			return rerr
		}
		if n == 0 {
			return ErrNoSuchGroup
		}
		return nil
	})
	if err != nil && !errors.Is(err, ErrNameTaken) && !errors.Is(err, ErrNoSuchGroup) {
		return fmt.Errorf("renaming a group: %w", err)
	}
	return err
}

// DeleteGroup removes a group. Its memberships and the grants naming it
// cascade with it.
func (d *DB) DeleteGroup(ctx context.Context, id int64) error {
	err := d.Write(ctx, func(tx *sql.Tx) error {
		res, derr := tx.ExecContext(ctx, sqlDeleteGroup, id)
		if derr != nil {
			return derr
		}
		n, rerr := res.RowsAffected()
		if rerr != nil {
			return rerr
		}
		if n == 0 {
			return ErrNoSuchGroup
		}
		return nil
	})
	if err != nil && !errors.Is(err, ErrNoSuchGroup) {
		return fmt.Errorf("deleting a group: %w", err)
	}
	return err
}

// AddMembership puts an account in a group. Adding twice is not an error: the
// caller asked for a state, and the state is reached either way.
func (d *DB) AddMembership(ctx context.Context, userID, groupID int64) error {
	if err := d.f.EnsureWritable(); err != nil {
		return err
	}
	if err := d.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, sqlAddMembership, userID, groupID)
		return err
	}); err != nil {
		return fmt.Errorf("adding a membership: %w", err)
	}
	return nil
}

// RemoveMembership takes an account out of a group.
func (d *DB) RemoveMembership(ctx context.Context, userID, groupID int64) error {
	if err := d.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, sqlRemoveMembership, userID, groupID)
		return err
	}); err != nil {
		return fmt.Errorf("removing a membership: %w", err)
	}
	return nil
}

// SetMemberships replaces the whole set an account belongs to, in one
// transaction, so a screen that submits a list never leaves the account in a
// state neither the old list nor the new one describes.
func (d *DB) SetMemberships(ctx context.Context, userID int64, groupIDs []int64) error {
	if err := d.f.EnsureWritable(); err != nil {
		return err
	}
	if err := d.Write(ctx, func(tx *sql.Tx) error {
		if _, derr := tx.ExecContext(ctx, sqlDeleteMembershipsOfUser, userID); derr != nil {
			return derr
		}
		for _, g := range groupIDs {
			if _, ierr := tx.ExecContext(ctx, sqlAddMembership, userID, g); ierr != nil {
				return fmt.Errorf("group %d: %w", g, ierr)
			}
		}
		return nil
	}); err != nil {
		return fmt.Errorf("setting memberships: %w", err)
	}
	return nil
}

// GroupIDsOf returns the groups an account belongs to.
func (d *DB) GroupIDsOf(ctx context.Context, userID int64) (out []int64, err error) {
	rows, err := d.f.SQL().QueryContext(ctx, sqlMembershipsOfUser, userID)
	if err != nil {
		return nil, fmt.Errorf("reading memberships: %w", err)
	}
	defer func() { err = errors.Join(err, rows.Close()) }()

	for rows.Next() {
		var g int64
		if serr := rows.Scan(&g); serr != nil {
			return nil, fmt.Errorf("reading a membership: %w", serr)
		}
		out = append(out, g)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("reading memberships: %w", err)
	}
	return out, nil
}

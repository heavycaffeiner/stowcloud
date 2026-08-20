//go:build compat_nc

package nc

import (
	"context"
	"math"

	"github.com/heavycaffeiner/stowcloud/go/internal/compat/ncport"
)

// The account surfaces: the caller's own record, and another account's public
// half for a name lookup.

// The quota sentinels. These are not general-purpose: clients render all three
// differently, and reporting zero for unlimited makes every client display a
// full disk and refuse to upload.
const (
	// SpaceUnlimited means no per-user cap.
	SpaceUnlimited = -3
	// SpaceUnknown means the size could not be determined.
	SpaceUnknown = -2
)

// QuotaVal builds the quota block.
//
// The key order is the reference's. The unlimited branch is easy to get subtly
// wrong in a way that is invisible until a phone stops uploading: the sentinel
// belongs in the quota field alone, and the free field carries the storage's
// real free space with the total derived from it.
//
// Sending the sentinel for all three is what broke this once. The Android
// client compares a file's size against the free field before it will start an
// upload, and a negative free space is smaller than any file, so a large
// upload sat at a pending state forever without ever issuing a request.
func QuotaVal(q ncport.Quota) Val {
	if q.Total != nil && *q.Total > 0 {
		total := *q.Total
		var free uint64
		if total > q.Used {
			free = total - q.Used
		}
		return VMap(
			F("free", bytesVal(free)),
			F("used", bytesVal(q.Used)),
			F("total", bytesVal(total)),
			F("relative", VFloat(percent(q.Used, total))),
			F("quota", bytesVal(total)),
		)
	}

	total := q.Free + q.Used
	return VMap(
		F("free", bytesVal(q.Free)),
		F("used", bytesVal(q.Used)),
		F("total", bytesVal(total)),
		F("relative", VFloat(percent(q.Used, total))),
		F("quota", VInt(SpaceUnlimited)),
	)
}

// bytesVal renders a byte count.
//
// Clamped rather than reinterpreted. A count above the signed range would
// otherwise render negative, and a negative free space is smaller than any
// file: the Android client compares a file's size against it before starting
// an upload, so the upload would sit pending forever. No real filesystem
// reports one, which is exactly why an unclamped conversion would go unnoticed
// until it did.
func bytesVal(v uint64) Val {
	if v > math.MaxInt64 {
		return VInt(math.MaxInt64)
	}
	return VInt(int64(v))
}

// percent is the used fraction as a percentage with two decimals, which is
// what the reference rounds to.
func percent(used, total uint64) float64 {
	if total == 0 {
		return 0
	}
	return math.Round(float64(used)/float64(total)*10000) / 100
}

// CurrentUser is the caller's own record.
func CurrentUser(u ncport.UserInfo, q ncport.Quota) Val {
	return VMap(
		F("id", VStr(u.LoginName)),
		F("enabled", VBool(u.Enabled)),
		F("quota", QuotaVal(q)),
		F("email", optionalStr(u.Email)),

		// Both spellings. Clients are split: older ones read the hyphenated
		// key and newer ones the plain one, so emitting one only is a silently
		// blank account name in half the client population.
		F("displayname", VStr(u.DisplayName)),
		F("display-name", VStr(u.DisplayName)),

		F("phone", VStr("")),
		F("address", VStr("")),
		F("website", VStr("")),
		F("twitter", VStr("")),
		F("fediverse", VStr("")),
		F("organisation", VStr("")),
		F("role", VStr("")),
		F("headline", VStr("")),
		F("biography", VStr("")),
		F("profile_enabled", VBool(false)),
		F("groups", strList(u.Groups)),
		F("language", VStr(u.Language)),
		F("locale", VStr(u.Locale)),
		F("notify_email", VNull()),

		F("backendCapabilities", VMap(
			// A client is never allowed to change a password or a display name
			// through this surface, and saying so up front stops it rendering
			// edit affordances that would be refused.
			F("setDisplayName", VBool(false)),
			F("setPassword", VBool(false)),
		)),
	)
}

// OtherUser is another account's public half, for the lookup both apps do when
// they need to turn a login into a display name.
//
// The same shape minus the quota, which is nobody else's business, and minus
// the backend capabilities, which describe what the caller may change about
// their own account.
func OtherUser(u ncport.UserInfo) Val {
	return VMap(
		F("id", VStr(u.LoginName)),
		F("enabled", VBool(u.Enabled)),
		F("email", optionalStr(u.Email)),
		F("displayname", VStr(u.DisplayName)),
		F("display-name", VStr(u.DisplayName)),
		F("groups", strList(u.Groups)),
	)
}

// currentUser answers the caller's own record.
func (l *Layer) currentUser(ctx context.Context, user ncport.UserID) (Val, *OCSError) {
	if l.deps.Accounts == nil {
		return Val{}, ServerError("could not read account")
	}
	info, err := l.deps.Accounts.UserInfo(ctx, user)
	if err != nil {
		return Val{}, ServerError("could not read account")
	}
	quota, err := l.deps.Accounts.Quota(ctx, user)
	if err != nil {
		return Val{}, ServerError("could not read account")
	}
	return CurrentUser(info, quota), nil
}

// otherUser answers a lookup by login name.
//
// Outside the configured scope and no such account are the same answer, and
// that is the point: a different one tells a caller which logins exist.
func (l *Layer) otherUser(ctx context.Context, caller ncport.UserID, login string) (Val, *OCSError) {
	notFound := NotFound("User does not exist")
	if l.deps.Accounts == nil || login == "" || containsSlash(login) {
		return Val{}, notFound
	}

	// The caller's own account is always in scope: asking about yourself
	// reveals nothing you do not already hold.
	if own, err := l.deps.Accounts.UserInfo(ctx, caller); err == nil && own.LoginName == login {
		return OtherUser(own), nil
	}

	info, ok, err := l.deps.Accounts.UserInfoByLogin(ctx, caller, login)
	if err != nil || !ok {
		return Val{}, notFound
	}
	return OtherUser(info), nil
}

func optionalStr(s *string) Val {
	if s == nil {
		return VNull()
	}
	return VStr(*s)
}

func containsSlash(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] == '/' {
			return true
		}
	}
	return false
}

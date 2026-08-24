//go:build linux && compat_nc

package nc

import (
	"math"
	"testing"

	"github.com/heavycaffeiner/stowcloud/go/internal/compat/ncport"
)

// The quota block is the one where a wrong value stops a phone uploading
// without any error a user can see.

func quotaField(t *testing.T, q ncport.Quota, key string) Val {
	t.Helper()
	got, ok := QuotaVal(q).Get(key)
	if !ok {
		t.Fatalf("the quota block has no %q", key)
	}
	return got
}

// The unlimited branch is where this went wrong once. The sentinel belongs in
// the quota field alone: the free field carries the storage's real free space,
// because the Android client compares a file's size against it before it will
// start an upload, and a negative free space is smaller than any file.
func TestUnlimitedPutsTheSentinelInTheQuotaFieldOnly(t *testing.T) {
	q := ncport.Quota{Used: 100, Free: 900, Total: nil}

	if got := quotaField(t, q, "quota"); got.Int != SpaceUnlimited {
		t.Fatalf("quota = %d, want the unlimited sentinel", got.Int)
	}
	if got := quotaField(t, q, "free"); got.Int != 900 {
		t.Fatalf("free = %d, want the real free space", got.Int)
	}
	if got := quotaField(t, q, "used"); got.Int != 100 {
		t.Fatalf("used = %d", got.Int)
	}
	// The total is derived from the two, not the sentinel.
	if got := quotaField(t, q, "total"); got.Int != 1000 {
		t.Fatalf("total = %d, want free plus used", got.Int)
	}
}

// A negative free space is what stalls an upload, so no branch may produce one.
func TestNoBranchReportsANegativeFreeSpace(t *testing.T) {
	capped := uint64(500)
	for _, q := range []ncport.Quota{
		{Used: 100, Free: 900, Total: nil},
		{Used: 100, Free: 900, Total: &capped},
		// Over quota, which is an ordinary state after an administrator
		// lowers a cap.
		{Used: 900, Free: 100, Total: &capped},
		{},
	} {
		if got := quotaField(t, q, "free"); got.Int < 0 {
			t.Fatalf("free = %d for %+v; a negative free space is smaller than "+
				"any file and the upload never starts", got.Int, q)
		}
	}
}

// An account over its cap reports no free space rather than a negative one.
func TestBeingOverQuotaReportsZeroFree(t *testing.T) {
	capped := uint64(500)
	q := ncport.Quota{Used: 900, Free: 100, Total: &capped}
	if got := quotaField(t, q, "free"); got.Int != 0 {
		t.Fatalf("free = %d, want 0", got.Int)
	}
	if got := quotaField(t, q, "used"); got.Int != 900 {
		t.Fatalf("used = %d", got.Int)
	}
}

// A byte count above the signed range is clamped rather than reinterpreted. No
// real filesystem reports one, which is exactly why an unclamped conversion
// would go unnoticed until it did.
func TestAnAbsurdByteCountIsClampedNotWrapped(t *testing.T) {
	huge := uint64(math.MaxUint64)
	q := ncport.Quota{Used: 0, Free: huge, Total: nil}
	got := quotaField(t, q, "free")
	if got.Int < 0 {
		t.Fatalf("free = %d, which wrapped negative", got.Int)
	}
	if got.Int != math.MaxInt64 {
		t.Fatalf("free = %d, want it clamped", got.Int)
	}
}

// The key order is the reference's, because several clients read it
// positionally.
func TestTheQuotaKeyOrder(t *testing.T) {
	v := QuotaVal(ncport.Quota{Used: 1, Free: 2})
	want := []string{"free", "used", "total", "relative", "quota"}
	if len(v.Map) != len(want) {
		t.Fatalf("the block has %d keys, want %d", len(v.Map), len(want))
	}
	for i, key := range want {
		if v.Map[i].Key != key {
			t.Fatalf("key %d is %q, want %q", i, v.Map[i].Key, key)
		}
	}
}

// The relative figure is a percentage with two decimals, and an integral one
// renders without a trailing decimal.
func TestTheRelativeFigureIsAPercentage(t *testing.T) {
	capped := uint64(1000)
	got := quotaField(t, ncport.Quota{Used: 250, Total: &capped}, "relative")
	if got.Float != 25 {
		t.Fatalf("relative = %v, want 25", got.Float)
	}
	// And a division by nothing is zero rather than a not-a-number that would
	// break the client's parser.
	if got := quotaField(t, ncport.Quota{}, "relative"); got.Float != 0 {
		t.Fatalf("relative = %v for an empty quota, want 0", got.Float)
	}
}

// Both spellings of the display name. Clients are split, so emitting one only
// is a silently blank account name in half the client population.
func TestTheDisplayNameIsEmittedBothWays(t *testing.T) {
	u := ncport.UserInfo{LoginName: "alice", DisplayName: "Alice", Enabled: true}
	v := CurrentUser(u, ncport.Quota{})

	for _, key := range []string{"displayname", "display-name"} {
		got, ok := v.Get(key)
		if !ok || got.Str != "Alice" {
			t.Fatalf("%s = %v, want Alice", key, got)
		}
	}

	// And on the other-account shape too, for the same split population.
	other := OtherUser(u)
	for _, key := range []string{"displayname", "display-name"} {
		got, ok := other.Get(key)
		if !ok || got.Str != "Alice" {
			t.Fatalf("the other-account %s = %v", key, got)
		}
	}
}

// Another account's record carries no quota, which is nobody else's business,
// and no backend capabilities, which describe what the caller may change about
// their own account.
func TestAnotherAccountCarriesNoQuota(t *testing.T) {
	v := OtherUser(ncport.UserInfo{LoginName: "bob"})
	for _, absent := range []string{"quota", "backendCapabilities"} {
		if _, present := v.Get(absent); present {
			t.Fatalf("another account's record carries %q", absent)
		}
	}
}

// A client is never allowed to change a password or a display name through
// this surface, and saying so up front stops it rendering edit affordances
// that would be refused.
func TestTheBackendCapabilitiesSayNo(t *testing.T) {
	v := CurrentUser(ncport.UserInfo{LoginName: "alice"}, ncport.Quota{})
	for _, key := range []string{"setDisplayName", "setPassword"} {
		got, ok := v.Path("backendCapabilities." + key)
		if !ok || got.Bool {
			t.Fatalf("backendCapabilities.%s = %v, want false", key, got)
		}
	}
}

// An absent email is null rather than an empty string: the two mean different
// things to a client rendering a profile.
func TestAnAbsentEmailIsNull(t *testing.T) {
	v := CurrentUser(ncport.UserInfo{LoginName: "alice"}, ncport.Quota{})
	got, ok := v.Get("email")
	if !ok || got.Kind != KindNull {
		t.Fatalf("email = %v, want null", got)
	}

	addr := "alice@example"
	withEmail := CurrentUser(ncport.UserInfo{LoginName: "alice", Email: &addr}, ncport.Quota{})
	got, ok = withEmail.Get("email")
	if !ok || got.Str != addr {
		t.Fatalf("email = %v, want the address", got)
	}
}

// Linux only, for the same reason as the rest of this package.
//go:build linux

// The account family's projections: live sessions and app passwords.
//
// Both list credentials without carrying them. A session row holds the stored
// digest and an app password holds none at all, so the only thing a listing
// can give away is which devices are signed in, which is the point of showing
// it.
package handler

import (
	"crypto/sha256"
	"encoding/hex"
	"strconv"

	"github.com/heavycaffeiner/stowcloud/go/engine/service/acl"
	"github.com/heavycaffeiner/stowcloud/go/engine/service/auth"
	"github.com/heavycaffeiner/stowcloud/go/engine/service/core"
)

// SessionView is one live session as its owner sees it.
//
// There is no token. The handle a client revokes with is a digest of the
// stored digest, so the listing identifies a session without holding anything
// that could resume one.
type SessionView struct {
	// Handle names this session for revocation. It is derived from the stored
	// digest rather than being it, so a leaked listing yields neither the
	// token nor the value the store compares against.
	Handle string `json:"handle"`

	CreatedNs  string `json:"created_ns"`
	LastSeenNs string `json:"last_seen_ns"`
	AbsoluteNs string `json:"absolute_ns"`

	// IP and UA are what the client itself presented. They are shown so a
	// person can recognise their own devices, which is the whole reason the
	// screen exists.
	IP string `json:"ip,omitempty"`
	UA string `json:"ua,omitempty"`

	// Current marks the session making this request, so a client can warn
	// before signing itself out.
	Current bool `json:"current"`
}

// AppPasswordView is one app password.
//
// The secret appears once, in the mint response, and never here.
type AppPasswordView struct {
	ID    string   `json:"id"`
	Name  string   `json:"name"`
	Perms []string `json:"perms"`

	// Shares is which shares this credential reaches. Empty means every share
	// the account itself reaches, which is a different thing from none.
	Shares []string `json:"shares"`

	CreatedNs string `json:"created_ns"`

	// ExpiresNs is absent when the credential does not expire, and LastUsedNs
	// when it has never been used. Zero is a real instant for both.
	ExpiresNs  *string `json:"expires_ns,omitempty"`
	LastUsedNs *string `json:"last_used_ns,omitempty"`
}

// MintedAppPasswordView is the one response that carries the secret.
//
// A separate type, for the same reason the minted link is: a field that is
// usually empty is a field one handler forgets to clear.
type MintedAppPasswordView struct {
	AppPassword AppPasswordView `json:"app_password"`
	Secret      string          `json:"secret"`
}

// SessionHandle derives the revocation handle from a session's stored digest.
//
// A second hash rather than the digest itself. The store compares against the
// digest, so publishing it would hand out the value that authenticates a
// lookup; hashing it again yields a stable name that authenticates nothing.
func SessionHandle(idHash []byte) string {
	sum := sha256.Sum256(idHash)
	return hex.EncodeToString(sum[:])
}

// SessionOf projects one session.
//
// currentHash is the stored digest of the session making the request, so the
// row for it can be marked. Comparing derived handles rather than digests
// keeps the digest out of the caller's hands too.
func SessionOf(r auth.SessionRow, currentHash []byte) SessionView {
	handle := SessionHandle(r.IDHash)
	return SessionView{
		Handle:     handle,
		CreatedNs:  strconv.FormatInt(r.CreatedNs, 10),
		LastSeenNs: strconv.FormatInt(r.LastSeenNs, 10),
		AbsoluteNs: strconv.FormatInt(r.AbsoluteNs, 10),
		IP:         r.IP,
		UA:         r.UA,
		// The length check is belt and braces: with no current session the
		// comparison is against the hash of nothing, which no real digest
		// produces. Named so the comparison is not mistaken for the guard.
		Current: len(currentHash) > 0 && handle == SessionHandle(currentHash),
	}
}

// SessionsOf projects a listing.
func SessionsOf(rows []auth.SessionRow, currentHash []byte) []SessionView {
	out := make([]SessionView, 0, len(rows))
	for _, r := range rows {
		out = append(out, SessionOf(r, currentHash))
	}
	return out
}

// AppPasswordOf projects one credential.
func AppPasswordOf(r auth.AppPasswordRow) AppPasswordView {
	v := AppPasswordView{
		ID:        strconv.FormatInt(r.ID, 10),
		Name:      r.Name,
		Perms:     core.PermNames(acl.Perms(r.ScopePerms)),
		Shares:    append([]string(nil), r.Shares...),
		CreatedNs: strconv.FormatInt(r.CreatedNs, 10),
	}
	if v.Shares == nil {
		v.Shares = []string{}
	}
	if r.ExpiresNs != nil {
		e := strconv.FormatInt(*r.ExpiresNs, 10)
		v.ExpiresNs = &e
	}
	if r.LastUsedNs != nil {
		u := strconv.FormatInt(*r.LastUsedNs, 10)
		v.LastUsedNs = &u
	}
	return v
}

// AppPasswordsOf projects a listing.
func AppPasswordsOf(rows []auth.AppPasswordRow) []AppPasswordView {
	out := make([]AppPasswordView, 0, len(rows))
	for _, r := range rows {
		out = append(out, AppPasswordOf(r))
	}
	return out
}

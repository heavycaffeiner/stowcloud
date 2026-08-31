// Linux only, for the same reason as the rest of this package.
//go:build linux

// The share-link projection.
//
// This one carries a credential, so the shape is a security decision rather
// than a formatting one. The token authenticates anyone holding it, and the
// hash authenticates a request; neither belongs in a listing an owner reads
// twice a day.
package handler

import (
	"strconv"

	"github.com/heavycaffeiner/stowcloud/go/engine/service/core"
)

// LinkView is one share link as its owner reads it.
//
// There is no token field. A link's token is shown once, at the moment it is
// minted, by the mint response below; a listing that carried it would put a
// live credential in every cache, log and screenshot of that page.
type LinkView struct {
	ID    string `json:"id"`
	Path  string `json:"path"`
	Share string `json:"share"`
	Label string `json:"label,omitempty"`
	Note  string `json:"note,omitempty"`

	// Perms is the link's permission set, by name.
	Perms []string `json:"perms"`

	// Drop marks a link that can receive files and can never list them. It is
	// derived rather than left to a client to infer from the permission names,
	// because getting it wrong means showing a file browser for a mailbox.
	Drop bool `json:"drop"`

	// HasPassword is the only password fact that leaves the service. Whether
	// one is set changes what the screen offers; anything more is the
	// password.
	HasPassword bool `json:"has_password"`

	// ExpiresNs is absent when the link never expires, since zero would be a
	// real instant in 1970 and every link would read as long expired.
	ExpiresNs *string `json:"expires_ns,omitempty"`
	CreatedNs string  `json:"created_ns"`

	// MaxDownloads is absent when unlimited. Downloads is the count so far,
	// always present, since zero downloads is a real answer.
	MaxDownloads *string `json:"max_downloads,omitempty"`
	Downloads    string  `json:"downloads"`

	// Expired and Exhausted say why a link will not serve, separately: one is
	// fixed by extending it and the other by raising the cap.
	Expired   bool `json:"expired"`
	Exhausted bool `json:"exhausted"`
}

// MintedLinkView is the one response that carries the token.
//
// A separate type from LinkView rather than a field on it. A field would be
// absent in listings and present here, and one handler forgetting to clear it
// is all it takes; a type that has no such field cannot leak one.
type MintedLinkView struct {
	Link  LinkView `json:"link"`
	Token string   `json:"token"`
}

// LinkOf projects one link for its owner.
//
// nowNs decides expiry, passed in rather than read here so a listing renders
// every row against one instant instead of drifting across the page.
func LinkOf(l core.Link, nowNs int64) LinkView {
	v := LinkView{
		ID:          strconv.FormatInt(l.ID, 10),
		Path:        l.Path.String(),
		Share:       strconv.FormatUint(uint64(l.Share), 10),
		Label:       l.Label,
		Note:        l.Note,
		Perms:       core.PermNames(l.Perms),
		Drop:        l.IsDrop(),
		HasPassword: l.HasPassword,
		CreatedNs:   strconv.FormatInt(l.CreatedNs, 10),
		Downloads:   strconv.FormatInt(int64(l.Downs), 10),
	}
	if l.Expires != 0 {
		e := strconv.FormatInt(l.Expires, 10)
		v.ExpiresNs = &e
		v.Expired = l.Expires <= nowNs
	}
	if l.MaxDown >= 0 {
		m := strconv.FormatInt(int64(l.MaxDown), 10)
		v.MaxDownloads = &m
		v.Exhausted = l.Downs >= l.MaxDown
	}
	return v
}

// LinksOf projects a listing.
func LinksOf(links []core.Link, nowNs int64) []LinkView {
	out := make([]LinkView, 0, len(links))
	for _, l := range links {
		out = append(out, LinkOf(l, nowNs))
	}
	return out
}

// MintedLinkOf projects a link that was just created, with its token.
//
// Returns ok false when the token could not be recovered, which is the legacy
// case the service reports with a nil token. The caller answers with the
// listing shape instead of inventing a token or sending an empty one, since an
// empty string in that field is a token a client would try to use.
func MintedLinkOf(l core.Link, nowNs int64) (MintedLinkView, bool) {
	if l.Token == nil {
		return MintedLinkView{}, false
	}
	return MintedLinkView{
		Link:  LinkOf(l, nowNs),
		Token: string(l.Token.Reveal()),
	}, true
}

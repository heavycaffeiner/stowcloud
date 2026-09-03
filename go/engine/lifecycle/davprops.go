//go:build linux

package lifecycle

import (
	"context"
	"fmt"
	"github.com/heavycaffeiner/stowcloud/go/engine/http/dav"
	"github.com/heavycaffeiner/stowcloud/go/engine/http/middleware"
	"github.com/heavycaffeiner/stowcloud/go/engine/service/core"
	"github.com/heavycaffeiner/stowcloud/go/engine/store/ident"
	"github.com/heavycaffeiner/stowcloud/go/engine/store/state"
	"strings"
)

// Joining the WebDAV property surface to the rows that hold it.
//
// The protocol package names an opaque key and its own property type, because
// it may not import the storage tier. This is the one place that sees both, so
// the translation lives here.

// DavProps adapts the state database to the property store dav expects.
type DavProps struct{ db *state.DB }

// NewDavProps wraps a state database.
func NewDavProps(db *state.DB) *DavProps { return &DavProps{db: db} }

// DavKeyOf is the key a property row is stored under.
//
// The filesystem identity rather than the path, so a property survives a
// rename: what a client stored against a file belongs to that file, not to the
// name it happened to have.
func DavKeyOf(e core.Entry) dav.ResourceKey { return e.Ident }

// Props reads what is stored against a resource.
func (p *DavProps) Props(ctx context.Context, key dav.ResourceKey) ([]dav.StoredProp, error) {
	id, ok := key.(ident.Ident)
	if !ok {
		return nil, fmt.Errorf("a property key of type %T", key)
	}
	rows, err := p.db.DavProps(ctx, id)
	if err != nil {
		return nil, err
	}
	out := make([]dav.StoredProp, 0, len(rows))
	for _, r := range rows {
		out = append(out, dav.StoredProp{NS: r.NS, Name: r.Name, Value: r.Value})
	}
	return out, nil
}

// SetProps applies a whole instruction set.
//
// The atomicity is the database's: it writes them in one transaction, which is
// what RFC 4918 asks of PROPPATCH and what this layer would have no way to
// provide by looping.
func (p *DavProps) SetProps(ctx context.Context, key dav.ResourceKey, ops []dav.PropWrite) error {
	id, ok := key.(ident.Ident)
	if !ok {
		return fmt.Errorf("a property key of type %T", key)
	}
	rows := make([]state.DavPropOp, 0, len(ops))
	for _, o := range ops {
		rows = append(rows, state.DavPropOp{
			NS: o.NS, Name: o.Name, Value: o.Value, Remove: o.Remove,
		})
	}
	favPath := ""
	if pStr, ok := ctx.Value(keyDavPath).(string); ok {
		favPath = strings.TrimPrefix(pStr, "/dav/files/")
		favPath = strings.TrimPrefix(favPath, "/dav/")
		favPath = strings.TrimPrefix(favPath, "/")
	}
	if princ, pok := ctx.Value(middleware.KeyCredential).(middleware.Principal); pok && princ.UserID != 0 {
		for _, o := range ops {
			if (o.NS == "http://owncloud.org/ns" || o.NS == "http://nextcloud.org/ns") && (o.Name == "favorite" || o.Name == "is-favorite") {
				on := !o.Remove && o.Value == "1"
				if ferr := p.db.SetFavorite(ctx, princ.UserID, state.Favorite{Ident: id, Path: favPath}, on); ferr != nil {
					return ferr
				}
			}
		}
	}
	return p.db.SetDavProps(ctx, id, rows)
}

// DropProps discards a resource's properties.
func (p *DavProps) DropProps(ctx context.Context, key dav.ResourceKey) error {
	id, ok := key.(ident.Ident)
	if !ok {
		return fmt.Errorf("a property key of type %T", key)
	}
	return p.db.DropDavProps(ctx, id)
}

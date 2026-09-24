//go:build linux

package dav

import (
	"context"
	"fmt"

	core "github.com/heavycaffeiner/stowcloud/go/internal/feature/files"
	"github.com/heavycaffeiner/stowcloud/go/internal/platform/database/ident"
	"github.com/heavycaffeiner/stowcloud/go/internal/platform/database/state"
)

// StateProps adapts the durable DAV property rows to the protocol property
// store. It belongs beside the protocol because it translates protocol values
// to database rows without making either tier import the application package.
type StateProps struct{ db *state.DB }

// NewStateProps wraps a state database.
func NewStateProps(db *state.DB) *StateProps { return &StateProps{db: db} }

// EntryKey names a property row by filesystem identity, so properties survive a
// rename.
func EntryKey(e core.Entry) ResourceKey { return e.Ident }

func (p *StateProps) Props(ctx context.Context, key ResourceKey) ([]StoredProp, error) {
	id, ok := key.(ident.Ident)
	if !ok {
		return nil, fmt.Errorf("a property key of type %T", key)
	}
	rows, err := p.db.DavProps(ctx, id)
	if err != nil {
		return nil, err
	}
	out := make([]StoredProp, 0, len(rows))
	for _, r := range rows {
		out = append(out, StoredProp{NS: r.NS, Name: r.Name, Value: r.Value})
	}
	return out, nil
}

func (p *StateProps) SetProps(ctx context.Context, key ResourceKey, ops []PropWrite) error {
	id, ok := key.(ident.Ident)
	if !ok {
		return fmt.Errorf("a property key of type %T", key)
	}
	rows := make([]state.DavPropOp, 0, len(ops))
	for _, o := range ops {
		rows = append(rows, state.DavPropOp{NS: o.NS, Name: o.Name, Value: o.Value, Remove: o.Remove})
	}
	return p.db.SetDavProps(ctx, id, rows)
}

func (p *StateProps) DropProps(ctx context.Context, key ResourceKey) error {
	id, ok := key.(ident.Ident)
	if !ok {
		return fmt.Errorf("a property key of type %T", key)
	}
	return p.db.DropDavProps(ctx, id)
}

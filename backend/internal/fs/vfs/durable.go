//go:build linux

package vfs

import (
	"errors"
	"fmt"

	local "github.com/stowcloud/storage/local"
)

type DurableOpts struct {
	Mode      uint32
	Owner     *Owner
	NoClobber bool
}
type Durable struct {
	Replaced     bool
	OwnerRestore error
}

func localDurableOpts(o DurableOpts) local.DurableOpts {
	var owner *local.Owner
	if o.Owner != nil {
		x := local.Owner{UID: o.Owner.UID, GID: o.Owner.GID}
		owner = &x
	}
	return local.DurableOpts{Mode: o.Mode, Owner: owner, NoClobber: o.NoClobber}
}
func fromLocalDurable(d local.Durable) Durable {
	return Durable{Replaced: d.Replaced, OwnerRestore: d.OwnerRestore}
}

func (r *ShareRoot) WriteDurable(p SafePath, opt DurableOpts, write func(*File) error) (Durable, error) {
	q, e := localPathFor(p)
	if e != nil {
		return Durable{}, e
	}
	d, e := r.backend.WriteDurable(q, localDurableOpts(opt), func(f *local.File) error { return write(WrapFile(f.OSFile())) })
	if e != nil {
		return Durable{}, mapLocalErr(e)
	}
	return fromLocalDurable(d), nil
}
func (r *ShareRoot) PublishPart(part, dest SafePath, replacing bool) (Durable, error) {
	p, e := localPathFor(part)
	if e != nil {
		return Durable{}, e
	}
	d, e := localPathFor(dest)
	if e != nil {
		return Durable{}, e
	}
	v, e := r.backend.PublishPart(p, d, replacing)
	if e != nil {
		return Durable{}, mapLocalErr(e)
	}
	return fromLocalDurable(v), nil
}
func (r *ShareRoot) Mkdir(p SafePath) error {
	q, e := localPathFor(p)
	if e != nil {
		return e
	}
	return mapLocalErr(r.backend.Mkdir(q))
}
func (r *ShareRoot) Rmdir(p SafePath) error {
	q, e := localPathFor(p)
	if e != nil {
		return e
	}
	return mapLocalErr(r.backend.Rmdir(q))
}
func (r *ShareRoot) Unlink(p SafePath) error {
	q, e := localPathFor(p)
	if e != nil {
		return e
	}
	return mapLocalErr(r.backend.Unlink(q))
}
func (r *ShareRoot) Rename(from, to SafePath, noReplace bool) error {
	f, e := localPathFor(from)
	if e != nil {
		return e
	}
	t, e := localPathFor(to)
	if e != nil {
		return e
	}
	return mapLocalErr(r.backend.Rename(f, t, noReplace))
}
func (r *ShareRoot) CreatePart(p SafePath) (*File, error) {
	q, e := localPathFor(p)
	if e != nil {
		return nil, e
	}
	f, e := r.backend.CreatePart(q)
	if e != nil {
		return nil, mapLocalErr(e)
	}
	return WrapFile(f.OSFile()), nil
}
func (r *ShareRoot) SetTimes(p SafePath, mtimeNs int64) error {
	q, e := localPathFor(p)
	if e != nil {
		return e
	}
	return mapLocalErr(r.backend.SetTimes(q, mtimeNs))
}

var _ = errors.Is
var _ = fmt.Errorf

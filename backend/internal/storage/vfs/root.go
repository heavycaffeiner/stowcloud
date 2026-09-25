//go:build linux

package vfs

import (
	"errors"
	"fmt"
	"os"

	local "github.com/stowcloud/storage/local"
)

// ShareRoot keeps product share identity and policy while delegating all Linux
// descriptor mechanics to the reusable public local backend.
type ShareRoot struct {
	id       ShareID
	anchor   *os.File
	backend  *local.RootHandle
	policy   SharePolicy
	dev      uint64
	fsType   FsType
	hasBtime bool
	scratch  bool
	host     string
	admitted map[uint64]struct{}
	scoped   *ShareRoot
	owner    *ShareRoot
}

func localPolicy(p SharePolicy) local.Policy {
	var owner *local.Owner
	if p.Chown != nil {
		o := local.Owner{UID: p.Chown.UID, GID: p.Chown.GID}
		owner = &o
	}
	return local.Policy{Symlink: local.SymlinkPolicy(p.Symlink), CrossMount: p.CrossMount, ModeFile: p.ModeFile, ModeDir: p.ModeDir, Chown: owner}
}
func fromLocalKind(k local.Kind) Kind {
	switch k {
	case local.KindFile:
		return KindFile
	case local.KindDir:
		return KindDir
	case local.KindSymlink:
		return KindSymlink
	default:
		return KindOther
	}
}
func fromLocalStat(s local.Stat) Stat {
	return Stat{Dev: s.Dev, Ino: s.Ino, BtimeNs: s.BtimeNs, MtimeNs: s.MtimeNs, CtimeNs: s.CtimeNs, Size: s.Size, Mode: s.Mode, UID: s.UID, GID: s.GID, Nlink: s.Nlink, Kind: fromLocalKind(s.Kind)}
}
func fromLocalAdmission(a local.Admission) Admission {
	return Admission(a)
}
func mapLocalErr(err error) error {
	if err == nil {
		return nil
	}
	switch {
	case errors.Is(err, local.ErrNotFound):
		return fmt.Errorf("local: %w", ErrNotFound)
	case errors.Is(err, local.ErrDenied):
		return fmt.Errorf("local: %w", ErrDenied)
	case errors.Is(err, local.ErrExists):
		return fmt.Errorf("local: %w", ErrExists)
	case errors.Is(err, local.ErrNotEmpty):
		return fmt.Errorf("local: %w", ErrNotEmpty)
	case errors.Is(err, local.ErrNoSpace):
		return fmt.Errorf("local: %w", ErrNoSpace)
	case errors.Is(err, local.ErrCrossDevice):
		return fmt.Errorf("local: %w", ErrCrossDevice)
	case errors.Is(err, local.ErrSymlinkDenied):
		return fmt.Errorf("local: %w", ErrSymlinkDenied)
	case errors.Is(err, local.ErrIsDirectory):
		return fmt.Errorf("local: %w", ErrIsDirectory)
	case errors.Is(err, local.ErrNotADirectory):
		return fmt.Errorf("local: %w", ErrNotADirectory)
	default:
		return err
	}
}

func openRequestShareRoot(id ShareID, host string, policy SharePolicy) (*ShareRoot, error) {
	b, err := local.OpenRequestRoot(host, localPolicy(policy))
	if err != nil {
		return nil, mapLocalErr(err)
	}
	return &ShareRoot{id: id, backend: b, policy: policy, dev: b.Dev(), fsType: FsType(b.FsType()), hasBtime: b.HasBtime(), host: host, admitted: map[uint64]struct{}{b.Dev(): {}}}, nil
}

func newShareRoot(id ShareID, host string, policy SharePolicy, b *local.RootHandle) (*ShareRoot, error) {
	r := &ShareRoot{id: id, backend: b, policy: policy, dev: b.Dev(), fsType: FsType(b.FsType()), hasBtime: b.HasBtime(), host: host, admitted: map[uint64]struct{}{b.Dev(): {}}}
	if policy.Symlink == SymlinkDeny {
		return r, nil
	}
	deny := policy
	deny.Symlink = SymlinkDeny
	scoped, err := openRequestShareRoot(id, host, deny)
	if err != nil {
		return nil, errors.Join(err, b.Close())
	}
	scoped.policy = policy
	scoped.owner = r
	r.scoped = scoped
	return r, nil
}

func OpenShareRoot(id ShareID, host string, policy SharePolicy) (*ShareRoot, error) {
	b, err := local.OpenRequestRoot(host, localPolicy(policy))
	if err != nil {
		return nil, mapLocalErr(err)
	}
	return newShareRoot(id, host, policy, b)
}
func RegisterShareRoot(id ShareID, host string, policy SharePolicy) (*ShareRoot, Admission, error) {
	b, a, err := local.RegisterRequestRoot(host, localPolicy(policy))
	if err != nil {
		if errors.Is(err, local.ErrSandboxDenied) {
			return nil, Admission{}, fmt.Errorf("%w: %w", ErrSandboxDenied, ErrDenied)
		}
		return nil, Admission{}, mapLocalErr(err)
	}
	r, err := newShareRoot(id, host, policy, b)
	if err != nil {
		return nil, Admission{}, err
	}
	return r, fromLocalAdmission(a), nil
}
func OpenScratchRoot(host string, policy SharePolicy) (*ShareRoot, error) {
	r, _, err := RegisterShareRoot(0, host, policy)
	if err != nil {
		return nil, err
	}
	r.scratch = true
	return r, nil
}
func (r *ShareRoot) Close() (err error) {
	if r.owner != nil {
		return nil
	}
	if r.scoped != nil {
		err = errors.Join(err, mapLocalErr(r.scoped.backend.Close()))
		r.scoped = nil
	}
	if r.backend != nil {
		err = errors.Join(err, mapLocalErr(r.backend.Close()))
	}
	if r.anchor != nil {
		err = errors.Join(err, r.anchor.Close())
	}
	return err
}

// RestrictSymlinks returns a cached capability for grant-scoped operations.
// Whole-share resolutions retain the configured policy; scoped ACL paths use
// this deny-symlink capability so links cannot widen a grant to a sibling.
func (r *ShareRoot) RestrictSymlinks() (Root, error) {
	if r.owner != nil || r.policy.Symlink == SymlinkDeny {
		return r, nil
	}
	if r.scoped == nil {
		return nil, fmt.Errorf("restrict symlinks: %w", ErrDenied)
	}
	return r.scoped, nil
}
func (r *ShareRoot) ID() ShareID         { return r.id }
func (r *ShareRoot) Policy() SharePolicy { return r.policy }
func (r *ShareRoot) Dev() uint64         { return r.dev }
func (r *ShareRoot) FsType() FsType      { return r.fsType }
func (r *ShareRoot) HasBtime() bool      { return r.hasBtime }
func (r *ShareRoot) IsScratch() bool     { return r.scratch }
func (r *ShareRoot) Alive() error {
	if r.backend == nil {
		return fmt.Errorf("probe root: %w", ErrNotFound)
	}
	return mapLocalErr(r.backend.Alive())
}

// Retained only as a compatibility probe for product tests. Real resolution is
// performed exclusively by local.RootHandle through openat2.
func (r *ShareRoot) admitDevice(_ *os.File, dev uint64, _ string) error {
	if _, ok := r.admitted[dev]; ok {
		return nil
	}
	r.admitted[dev] = struct{}{}
	return nil
}

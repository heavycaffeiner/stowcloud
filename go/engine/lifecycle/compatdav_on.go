//go:build linux && compat_nc

// The compatibility vocabulary the WebDAV mount carries when the tag is on:
// the alternative prefixes the clients mount, the header names the chunked
// upload collection reads, and the vendor properties the sync client's
// journal is keyed on.
package lifecycle

import (
	"context"
	"encoding/xml"

	"github.com/heavycaffeiner/stowcloud/go/engine/http/compat"
	"github.com/heavycaffeiner/stowcloud/go/engine/http/dav"
	"github.com/heavycaffeiner/stowcloud/go/engine/kit/num"
	"github.com/heavycaffeiner/stowcloud/go/engine/service/acl"
	"github.com/heavycaffeiner/stowcloud/go/engine/service/core"
	"github.com/heavycaffeiner/stowcloud/go/engine/store/cache"
)

// davAliases are the alternative mount points the sync clients address, with
// the segment that names the account dropped rather than checked.
//
// The uploads prefixes come first only for readability; the matcher takes the
// longest match, because "/remote.php/dav/uploads/" sits under the same root
// as the files tree and the shorter prefix would swallow it.
func (e *Engine) davAliases() []DavAlias {
	return []DavAlias{
		{Prefix: "/remote.php/dav/uploads/", Mount: DavUploadPrefix, DropSegments: 1},
		{Prefix: "/index.php/remote.php/dav/uploads/", Mount: DavUploadPrefix, DropSegments: 1},
		{Prefix: "/remote.php/dav/files/", DropSegments: 1},
		{Prefix: "/index.php/remote.php/dav/files/", DropSegments: 1},
		{Prefix: "/remote.php/webdav/", DropSegments: 0},
		{Prefix: "/index.php/remote.php/webdav/", DropSegments: 0},
	}
}

// davUploadHeaders names the headers the chunked upload collection reads.
//
// They are the other product's vocabulary, named here so that package reads
// whatever it is given and learns none of it. A partly filled set disables
// the collection, so all three travel together or none does.
func (e *Engine) davUploadHeaders() dav.UploadHeaders {
	return dav.UploadHeaders{
		TotalLength: "OC-Total-Length",
		MTime:       "X-OC-Mtime",
		ETag:        "OC-ETag",
	}
}

// davVendorProps contributes the properties the sync client reads on every
// PROPFIND.
//
// The instance id is read once, here, because it is minted once and never
// changes: a client that saw one value and then another re-syncs everything
// it holds. A failure to read it is a database this mount cannot answer
// through, so the source is absent rather than half-present, and every
// property the client asked for answers missing, which is the answer it
// already handles by skipping the entry.
func (e *Engine) davVendorProps() func(
	ctx context.Context, res core.Resolved, entry core.Entry, want []xml.Name,
) []dav.Prop {
	id, err := e.State.InstanceID(context.Background())
	if err != nil {
		e.logger.Warn("the instance id could not be read; the sync surfaces are absent",
			"error", err)
		return nil
	}

	source := compat.NewPropSource(compat.PropSourceDeps{
		InstanceID: func() string { return id },
		Shared:     func(compat.PropEntry) bool { return false },
	})

	return func(ctx context.Context, _ core.Resolved, entry core.Entry, want []xml.Name) []dav.Prop {
		// The file id is resolved here, where the entry's identity lives: the
		// recorded override wins, and otherwise the id is the pure
		// derivation the allocation policy answers for a first candidate.
		fileID, ferr := e.compatFileID(ctx, entry)
		if ferr != nil {
			e.logger.Warn("an entry reached property emission without a file id",
				"name", entry.Name, "error", ferr)
		}
		return source.Props(compat.PropEntry{
			IsDir:  entry.IsDir,
			Size:   entry.Size,
			Perms:  permBitsOf(entry.Perms),
			FileID: fileID,
		}, want)
	}
}

// compatFileID resolves the stable id a sync journal keys an entry on.
//
// A recorded override wins, because a past collision decision is never
// revisited. Otherwise the id is the pure derivation: the allocation policy
// answers the same value for a first candidate that finds no collision, and
// the collision cases are exactly the ones that left overrides behind.
//
// A read that allocated would give a client an id for a file that may not
// exist by the time it acts, and would write during a listing.
func (e *Engine) compatFileID(ctx context.Context, entry core.Entry) (uint64, error) {
	if recorded, ok, err := e.State.LookupFileID(ctx, entry.Ident); err != nil {
		return 0, err
	} else if ok {
		return num.Narrow[uint64](recorded)
	}
	return num.Narrow[uint64](cache.DeriveID(entry.Ident, 0))
}

// permBitsOf maps the core's permission bitset onto the renderer's.
//
// Seven of the eight bits transfer one for one; Download has no letter on
// this wire, because a client that can read a file can download it and the
// vocabulary simply has nothing to say about the difference.
func permBitsOf(p acl.Perms) compat.PermBits {
	var out compat.PermBits
	if p.Has(acl.Share) {
		out |= compat.PermShare
	}
	if p.Has(acl.Read) {
		out |= compat.PermRead
	}
	if p.Has(acl.Delete) {
		out |= compat.PermDelete
	}
	if p.Has(acl.Rename) {
		out |= compat.PermRename
	}
	if p.Has(acl.Move) {
		out |= compat.PermMove
	}
	if p.Has(acl.Create) {
		out |= compat.PermCreate
	}
	if p.Has(acl.Write) {
		out |= compat.PermWrite
	}
	return out
}

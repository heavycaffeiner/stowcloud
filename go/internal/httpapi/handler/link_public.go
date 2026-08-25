// Linux only: it depends on packages that are Linux only.
//go:build linux

package handler

import (
	"net/http"

	"github.com/heavycaffeiner/stowcloud/go/internal/acl"
	"github.com/heavycaffeiner/stowcloud/go/internal/apierr"
	"github.com/heavycaffeiner/stowcloud/go/internal/archive"
	"github.com/heavycaffeiner/stowcloud/go/internal/core"
	"github.com/heavycaffeiner/stowcloud/go/internal/limits"
)

// What a visitor holding a share link can do with it.
//
// Everything here takes the token from the path and nothing else: there is no
// session, and the link's own permissions are the whole of what is allowed.
// The password, when the link has one, is answered once through the unlock
// endpoint and remembered in a cookie scoped to that link.

// linkFor resolves the token and enforces the password gate.
//
// One helper rather than the same six lines in every handler: a public
// endpoint that forgot the gate would serve a locked link's bytes to anyone
// with the address.
func linkFor(r *http.Request, d Deps) (core.Link, error) {
	link, _, err := d.Core.LinkPublic(r.Context(), r.PathValue("token"))
	if err != nil {
		return core.Link{}, err
	}
	if link.HasPassword && !linkUnlocked(r, d, link) {
		return core.Link{}, apierr.BadRequest("fs.link_password", "password")
	}
	return link, nil
}

// LinkZip answers GET /s/{token}/zip, packing a shared folder.
//
// The status is committed on the first byte, so a bound reached partway
// through cannot become a refusal: the archive closes out carrying a marker
// entry instead, the same way the authenticated archive route does.
func LinkZip(d Deps) http.HandlerFunc {
	return Wrap(func(w http.ResponseWriter, r *http.Request) error {
		link, err := linkFor(r, d)
		if err != nil {
			return err
		}
		if !link.Perms.Has(acl.Download) {
			return apierr.BadRequest("fs.link_no_download", "path")
		}
		sub := r.URL.Query().Get("path")
		listing, lerr := d.Core.LinkBrowse(r.Context(), link, sub)
		if lerr != nil {
			return lerr
		}
		if !listing.IsDir {
			return apierr.BadRequest("fs.link_not_a_folder", "path")
		}
		if nerr := d.Core.NoteLinkDownload(r.Context(), link); nerr != nil {
			return nerr
		}

		w.Header().Set("Content-Type", "application/zip")
		w.Header().Set("Content-Disposition", contentDisposition(listing.Name+".zip"))
		zw := archive.NewWriter(w)
		var entries int64
		var packed uint64
		werr := d.Core.LinkArchiveWalk(r.Context(), link, sub, func(e core.WalkEntry, s *core.Stream) error {
			if entries >= limits.ArchivePackedEntries || packed >= limits.ArchivePackedBytes {
				return errArchiveTruncated
			}
			entries++
			if !e.Readable {
				return nil
			}
			if e.IsDir {
				return zw.AddDir(e.RelPath, e.MTimeNs)
			}
			n, aerr := zw.AddFile(e.RelPath, e.MTimeNs, s)
			packed += n
			return aerr
		})
		if werr != nil && werr != errArchiveTruncated {
			// The bytes are already going out, so this cannot become a status.
			// Closing the archive leaves the visitor a file they can open and
			// see is short, which beats a stream that simply stops.
			_ = zw.Close() //nolint:errcheck // the walk error is the answer.
			return nil
		}
		return zw.Close()
	})
}

// LinkDrop answers POST /s/{token}/drop, the upload half of a link.
//
// A drop link grants Create and not Read: whoever holds it can put a file in
// and cannot see what is already there. That is the whole point of one, so the
// permission check is the feature rather than a guard on it.
func LinkDrop(d Deps) http.HandlerFunc {
	return Wrap(func(w http.ResponseWriter, r *http.Request) error {
		link, err := linkFor(r, d)
		if err != nil {
			return err
		}
		if !link.Perms.Has(acl.Create) {
			return apierr.BadRequest("fs.link_no_upload", "name")
		}
		name := r.URL.Query().Get("name")
		if name == "" {
			return apierr.BadRequest("fs.link_no_name", "name")
		}
		entry, werr := d.Core.LinkDropFile(r.Context(), link, name, r.Body)
		if werr != nil {
			return werr
		}
		return writeJSON(w, http.StatusCreated, map[string]any{
			"name": entry.Name, "size": entry.Size,
		})
	})
}
